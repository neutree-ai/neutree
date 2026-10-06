package dbtest

import (
	"context"
	"crypto/md5" //nolint:gosec // mirrors md5() in public.derive_user_profile_name, not used for security
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover how api.handle_new_user names the profile of a user that
// GoTrue creates from an SSO login: the IdP claims land in user_metadata and
// there is no username, so the name is derived (migration 101).

var userProfileNameRe = regexp.MustCompile(`^[a-z0-9]([-a-z0-9.]*[a-z0-9])?$`)

func gotrueServiceToken(t *testing.T) string {
	t.Helper()

	token, err := storage.CreateServiceToken(TestJWTSecret)
	if err != nil {
		t.Fatalf("failed to mint a service token: %v", err)
	}

	return *token
}

// createSSOShapedUser creates a user through the GoTrue admin API with the
// given body (user_metadata shaped like IdP claims) and deletes it when the
// test ends.
func createSSOShapedUser(t *testing.T, body map[string]any) string {
	t.Helper()

	code, raw := gotrueRequest(t, http.MethodPost, "/admin/users", gotrueServiceToken(t), body)
	if code != http.StatusOK {
		t.Fatalf("POST /admin/users = HTTP %d, want 200: %s", code, raw)
	}

	var user struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(raw, &user); err != nil || user.ID == "" {
		t.Fatalf("failed to decode the created user %q: %v", raw, err)
	}

	t.Cleanup(func() {
		if code, raw := gotrueRequest(t, http.MethodDelete, "/admin/users/"+user.ID, gotrueServiceToken(t), nil); code != http.StatusOK {
			t.Errorf("failed to delete test user %s: HTTP %d: %s", user.ID, code, raw)
		}
	})

	return user.ID
}

func userProfileNames(t *testing.T, id string) (name, displayName string) {
	t.Helper()

	err := GetTestDB(t).QueryRowContext(context.Background(),
		`SELECT (metadata).name, (metadata).display_name FROM api.user_profiles WHERE id = $1`, id).
		Scan(&name, &displayName)
	if err != nil {
		t.Fatalf("no user profile for %s: %v", id, err)
	}

	return name, displayName
}

func md5Prefix(id string) string {
	sum := md5.Sum([]byte(id)) //nolint:gosec // see import
	return hex.EncodeToString(sum[:])[:6]
}

func TestUserProfileSSOName(t *testing.T) {
	run := fmt.Sprintf("%d", time.Now().UnixNano())

	t.Run("preferred_username is normalized, name claim is the display name", func(t *testing.T) {
		id := createSSOShapedUser(t, map[string]any{
			"email":         "sso-pref-" + run + "@corp.test.local",
			"email_confirm": true,
			"user_metadata": map[string]any{
				"preferred_username": "Zhang.San." + run,
				"name":               "Zhang San",
				"sub":                "idp-" + run,
			},
		})

		name, display := userProfileNames(t, id)
		if want := "zhang.san." + run; name != want {
			t.Errorf("name = %q, want %q", name, want)
		}

		if display != "Zhang San" {
			t.Errorf("display_name = %q, want %q", display, "Zhang San")
		}
	})

	t.Run("preferred_username is the display name when there is no name claim", func(t *testing.T) {
		id := createSSOShapedUser(t, map[string]any{
			"email":         "sso-pref2-" + run + "@corp.test.local",
			"email_confirm": true,
			"user_metadata": map[string]any{"preferred_username": "Li.Si." + run},
		})

		name, display := userProfileNames(t, id)
		if want := "li.si." + run; name != want {
			t.Errorf("name = %q, want %q", name, want)
		}

		if want := "Li.Si." + run; display != want {
			t.Errorf("display_name = %q, want %q", display, want)
		}
	})

	t.Run("email local part when there is no username claim", func(t *testing.T) {
		email := "Zhang.San+x" + run + "@corp.test.local"
		id := createSSOShapedUser(t, map[string]any{
			"email":         email,
			"email_confirm": true,
			"user_metadata": map[string]any{"email": email, "sub": "idp-" + run},
		})

		name, display := userProfileNames(t, id)
		if want := "zhang.san-x" + run; name != want {
			t.Errorf("name = %q, want %q", name, want)
		}

		if display != email {
			t.Errorf("display_name = %q, want %q", display, email)
		}
	})

	t.Run("no usable claim falls back to the user id", func(t *testing.T) {
		// GoTrue rejects a non-ASCII email, so the user has a phone and only a
		// non-ASCII name: nothing to derive a name from.
		id := createSSOShapedUser(t, map[string]any{
			"phone":         "1555" + run[len(run)-7:],
			"phone_confirm": true,
			"user_metadata": map[string]any{"name": "张三"},
		})

		name, display := userProfileNames(t, id)
		if want := "u-" + strings.ReplaceAll(id, "-", "")[:8]; name != want {
			t.Errorf("name = %q, want %q", name, want)
		}

		if display != "张三" {
			t.Errorf("display_name = %q, want %q", display, "张三")
		}
	})

	t.Run("same derived name gets a suffix", func(t *testing.T) {
		claims := map[string]any{"preferred_username": "Dup.User." + run}

		first := createSSOShapedUser(t, map[string]any{
			"email": "sso-dup1-" + run + "@corp.test.local", "email_confirm": true, "user_metadata": claims,
		})
		second := createSSOShapedUser(t, map[string]any{
			"email": "sso-dup2-" + run + "@corp.test.local", "email_confirm": true, "user_metadata": claims,
		})

		firstName, _ := userProfileNames(t, first)
		secondName, secondDisplay := userProfileNames(t, second)

		if want := "dup.user." + run; firstName != want {
			t.Errorf("first name = %q, want %q", firstName, want)
		}

		if want := "dup.user." + run + "-" + md5Prefix(second); secondName != want {
			t.Errorf("second name = %q, want %q", secondName, want)
		}

		if want := "Dup.User." + run; secondDisplay != want {
			t.Errorf("second display_name = %q, want %q", secondDisplay, want)
		}
	})

	t.Run("never takes admin", func(t *testing.T) {
		id := createSSOShapedUser(t, map[string]any{
			"email":         "sso-admin-" + run + "@corp.test.local",
			"email_confirm": true,
			"user_metadata": map[string]any{"preferred_username": "Admin"},
		})

		name, _ := userProfileNames(t, id)
		if want := "admin-" + md5Prefix(id); name != want {
			t.Errorf("name = %q, want %q", name, want)
		}

		// The seeded admin is still the only profile named admin, so the
		// delete protection keyed on that name still points at it alone.
		var admins int
		if err := GetTestDB(t).QueryRowContext(context.Background(),
			`SELECT count(*) FROM api.user_profiles WHERE (metadata).name = 'admin'`).Scan(&admins); err != nil {
			t.Fatalf("failed to count admin profiles: %v", err)
		}

		if admins != 1 {
			t.Errorf("%d profiles named admin, want 1", admins)
		}
	})

	t.Run("explicit invalid username is still rejected", func(t *testing.T) {
		email := "sso-bad-" + run + "@corp.test.local"

		code, raw := gotrueRequest(t, http.MethodPost, "/admin/users", gotrueServiceToken(t), map[string]any{
			"email":         email,
			"password":      "testpassword",
			"email_confirm": true,
			"user_metadata": map[string]any{"username": "Bad_Name", "preferred_username": "good.name"},
		})
		if code == http.StatusOK {
			var user struct {
				ID string `json:"id"`
			}
			if json.Unmarshal(raw, &user) == nil && user.ID != "" {
				gotrueRequest(t, http.MethodDelete, "/admin/users/"+user.ID, gotrueServiceToken(t), nil)
			}

			t.Fatalf("POST /admin/users with username Bad_Name = HTTP 200, want an error: %s", raw)
		}

		var exists bool
		if err := GetTestDB(t).QueryRowContext(context.Background(),
			`SELECT EXISTS(SELECT 1 FROM auth.users WHERE email = $1)`, email).Scan(&exists); err != nil {
			t.Fatalf("failed to look up the user: %v", err)
		}

		if exists {
			t.Error("auth.users row was kept although the profile was rejected")
		}
	})

	t.Run("a later login does not rename the profile", func(t *testing.T) {
		id := createSSOShapedUser(t, map[string]any{
			"email":         "sso-relogin-" + run + "@corp.test.local",
			"email_confirm": true,
			"user_metadata": map[string]any{"preferred_username": "First.Login." + run, "name": "First"},
		})

		// GoTrue refreshes user_metadata from the IdP claims on every login;
		// the admin update stands in for that.
		code, raw := gotrueRequest(t, http.MethodPut, "/admin/users/"+id, gotrueServiceToken(t), map[string]any{
			"user_metadata": map[string]any{"preferred_username": "Renamed." + run, "name": "Renamed"},
		})
		if code != http.StatusOK {
			t.Fatalf("PUT /admin/users/%s = HTTP %d, want 200: %s", id, code, raw)
		}

		name, display := userProfileNames(t, id)
		if want := "first.login." + run; name != want {
			t.Errorf("name after update = %q, want %q", name, want)
		}

		if display != "First" {
			t.Errorf("display_name after update = %q, want %q", display, "First")
		}
	})
}

// TestDeriveUserProfileName checks the normalization rules directly.
func TestDeriveUserProfileName(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	// Not a real user: nothing in user_profiles collides with these names.
	const id = "0a1b2c3d-0000-4000-8000-000000000000"

	fallback := "u-0a1b2c3d"
	long := strings.Repeat("a", 70)

	cases := []struct {
		desc  string
		meta  string
		email string
		want  string
	}{
		{"preferred_username wins", `{"preferred_username":"Pref","user_name":"un","email":"em@x.io"}`, "", "pref"},
		{"user_name next", `{"user_name":"GitHub_User","email":"em@x.io"}`, "", "github-user"},
		{"metadata email next", `{"email":"Meta.Mail@x.io"}`, "row@x.io", "meta.mail"},
		{"row email last", `{}`, "Row.Mail@x.io", "row.mail"},
		{"blank claims are skipped", `{"preferred_username":"  ","user_name":""}`, "e@x.io", "e"},
		{"runs collapse and ends trim", `{"preferred_username":"--Zhang__San!!.."}`, "", "zhang-san"},
		{"email as preferred_username", `{"preferred_username":"zhang.san@corp.com"}`, "", "zhang.san-corp.com"},
		{"truncated to 56", `{"preferred_username":"` + long + `"}`, "", strings.Repeat("a", 56)},
		{"truncation does not leave a separator", `{"preferred_username":"` + strings.Repeat("a", 55) + `-b"}`, "", strings.Repeat("a", 55)},
		{"non-ascii only", `{"preferred_username":"张三"}`, "", fallback},
		{"nothing usable", `{"name":"张三"}`, "", fallback},
		{"reserved admin", `{"preferred_username":"ADMIN"}`, "", "admin-" + md5Prefix(id)},
	}

	for _, tc := range cases {
		t.Run(tc.desc, func(t *testing.T) {
			var email any
			if tc.email != "" {
				email = tc.email
			}

			var got string
			if err := db.QueryRowContext(ctx,
				`SELECT public.derive_user_profile_name($1::uuid, $2::jsonb, $3)`, id, tc.meta, email).Scan(&got); err != nil {
				t.Fatalf("derive_user_profile_name failed: %v", err)
			}

			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}

			if !userProfileNameRe.MatchString(got) || len(got) > 63 {
				t.Errorf("%q is not a valid profile name", got)
			}
		})
	}
}
