package dbtest

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/golang-jwt/jwt/v4"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests pin down what neutree relies on from GoTrue, so that bumping the
// image in db/docker-compose.test.yml (and the deploy manifests that mirror it)
// fails here rather than on a live deployment: the auth schema it migrates, the
// session endpoints, the shape of the access token PostgREST and the gateway
// read, and the admin APIs the control plane calls.

// gotrueMinSchemaVersion is the newest auth migration shipped with GoTrue
// v2.197.0. Anything older means the container did not migrate the schema.
const gotrueMinSchemaVersion = 20260831180000

// gotrueRequest sends a request to the test GoTrue and returns the status code
// and raw body. token is sent as a bearer token when non-empty.
func gotrueRequest(t *testing.T, method, path, token string, body any) (int, []byte) {
	t.Helper()

	var reader io.Reader

	if body != nil {
		raw, err := json.Marshal(body)
		if err != nil {
			t.Fatalf("failed to encode the request: %v", err)
		}

		reader = strings.NewReader(string(raw))
	}

	req, err := http.NewRequest(method, GetGoTrueURL()+path, reader)
	if err != nil {
		t.Fatalf("failed to build the request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")

	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the response: %v", err)
	}

	return resp.StatusCode, raw
}

type gotrueSession struct {
	AccessToken  string `json:"access_token"`
	RefreshToken string `json:"refresh_token"`
}

// gotruePasswordLogin logs in through the same grant the UI uses and fails the
// test unless GoTrue hands back a session.
func gotruePasswordLogin(t *testing.T, email, password string) gotrueSession {
	t.Helper()

	code, body := gotrueRequest(t, http.MethodPost, "/token?grant_type=password", "",
		map[string]string{"email": email, "password": password})
	if code != http.StatusOK {
		t.Fatalf("password login = HTTP %d, want 200: %s", code, body)
	}

	var session gotrueSession
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("failed to decode the session %q: %v", body, err)
	}

	if session.AccessToken == "" || session.RefreshToken == "" {
		t.Fatalf("password login returned an incomplete session: %s", body)
	}

	return session
}

// createGoTrueTestUser creates a user the way neutree's POST /auth/admin/users
// does and deletes it again when the test ends. The email carries a per-run
// suffix so the test does not depend on a fresh database.
func createGoTrueTestUser(t *testing.T, username, password string) *TestUser {
	t.Helper()

	email := fmt.Sprintf("%s-%d@gotrue.test.local", username, time.Now().UnixNano())
	user := CreateTestUser(t, username, email, password)

	t.Cleanup(func() {
		token, err := storage.CreateServiceToken(TestJWTSecret)
		if err != nil {
			t.Errorf("failed to mint a service token: %v", err)
			return
		}

		// api.user_profiles references auth.users with ON DELETE CASCADE, so
		// this removes the profile row as well.
		if code, body := gotrueRequest(t, http.MethodDelete, "/admin/users/"+user.ID, *token, nil); code != http.StatusOK {
			t.Errorf("failed to delete test user %s: HTTP %d: %s", user.ID, code, body)
		}
	})

	return user
}

func TestGoTrue_SchemaMigrated(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	t.Run("latest migration applied", func(t *testing.T) {
		var version int64

		err := db.QueryRowContext(ctx, `SELECT max(version::bigint) FROM auth.schema_migrations`).Scan(&version)
		if err != nil {
			t.Fatalf("failed to read auth.schema_migrations: %v", err)
		}

		if version < gotrueMinSchemaVersion {
			t.Errorf("latest auth migration = %d, want >= %d", version, gotrueMinSchemaVersion)
		}
	})

	t.Run("factor_type enum lives in auth", func(t *testing.T) {
		// GoTrue creates its enums in DB_NAMESPACE; one landing in another
		// schema means the migrations ran with the wrong search path.
		rows, err := db.QueryContext(ctx, `
			SELECT n.nspname FROM pg_type t
			JOIN pg_namespace n ON n.oid = t.typnamespace
			WHERE t.typname = 'factor_type'`)
		if err != nil {
			t.Fatalf("failed to look up factor_type: %v", err)
		}
		defer rows.Close()

		var schemas []string

		for rows.Next() {
			var schema string
			if err := rows.Scan(&schema); err != nil {
				t.Fatalf("failed to scan schema: %v", err)
			}

			schemas = append(schemas, schema)
		}

		if err := rows.Err(); err != nil {
			t.Fatalf("failed to list factor_type schemas: %v", err)
		}

		if len(schemas) != 1 || schemas[0] != "auth" {
			t.Errorf("factor_type found in schemas %v, want only [auth]", schemas)
		}
	})

	t.Run("factor_type has recovery_code", func(t *testing.T) {
		var exists bool

		err := db.QueryRowContext(ctx, `
			SELECT EXISTS(
				SELECT 1 FROM pg_enum e
				JOIN pg_type t ON t.oid = e.enumtypid
				JOIN pg_namespace n ON n.oid = t.typnamespace
				WHERE n.nspname = 'auth' AND t.typname = 'factor_type' AND e.enumlabel = 'recovery_code'
			)`).Scan(&exists)
		if err != nil {
			t.Fatalf("failed to check factor_type labels: %v", err)
		}

		if !exists {
			t.Error("auth.factor_type has no recovery_code label")
		}
	})
}

func TestGoTrue_SessionLifecycle(t *testing.T) {
	user := createGoTrueTestUser(t, "gotrue-session", "testpassword")
	session := gotruePasswordLogin(t, user.Email, "testpassword")

	t.Run("refresh token grant", func(t *testing.T) {
		code, body := gotrueRequest(t, http.MethodPost, "/token?grant_type=refresh_token", "",
			map[string]string{"refresh_token": session.RefreshToken})
		if code != http.StatusOK {
			t.Fatalf("refresh = HTTP %d, want 200: %s", code, body)
		}
	})

	t.Run("get user", func(t *testing.T) {
		code, body := gotrueRequest(t, http.MethodGet, "/user", session.AccessToken, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /user = HTTP %d, want 200: %s", code, body)
		}
	})
}

// TestGoTrue_AccessTokenClaims checks the claims neutree reads from a GoTrue
// access token. Only presence is asserted: upstream adds claims over time.
func TestGoTrue_AccessTokenClaims(t *testing.T) {
	user := createGoTrueTestUser(t, "gotrue-claims", "testpassword")
	session := gotruePasswordLogin(t, user.Email, "testpassword")

	claims := jwt.MapClaims{}

	token, err := jwt.ParseWithClaims(session.AccessToken, claims, func(token *jwt.Token) (any, error) {
		if token.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method %v", token.Header["alg"])
		}

		return []byte(TestJWTSecret), nil
	})
	if err != nil {
		t.Fatalf("access token does not verify as HS256 with the shared secret: %v", err)
	}

	if !token.Valid {
		t.Fatal("access token is not valid")
	}

	for _, key := range []string{"sub", "role", "aud", "exp", "iat", "email", "app_metadata", "user_metadata", "session_id"} {
		if _, ok := claims[key]; !ok {
			t.Errorf("access token has no %q claim", key)
		}
	}

	// GOTRUE_JWT_DEFAULT_GROUP_NAME; PostgREST switches to this database role.
	if claims["role"] != "api_user" {
		t.Errorf("role claim = %v, want api_user", claims["role"])
	}

	if claims["sub"] != user.ID {
		t.Errorf("sub claim = %v, want %s", claims["sub"], user.ID)
	}
}

// TestGoTrue_AccessTokenWorksWithPostgREST checks the data plane accepts a
// GoTrue session: PostgREST verifies the token and auth.uid() resolves to the
// user, so RLS lets them read their own profile.
func TestGoTrue_AccessTokenWorksWithPostgREST(t *testing.T) {
	waitForPostgREST(t)

	user := createGoTrueTestUser(t, "gotrue-postgrest", "testpassword")
	session := gotruePasswordLogin(t, user.Email, "testpassword")

	req, err := http.NewRequest(http.MethodGet, GetPostgRESTURL()+"/user_profiles?id=eq."+url.QueryEscape(user.ID), nil)
	if err != nil {
		t.Fatalf("failed to build the request: %v", err)
	}

	req.Header.Set("Accept-Profile", "api")
	req.Header.Set("Authorization", "Bearer "+session.AccessToken)

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the response: %v", err)
	}

	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /user_profiles = HTTP %d, want 200: %s", resp.StatusCode, body)
	}

	var rows []json.RawMessage
	if err := json.Unmarshal(body, &rows); err != nil {
		t.Fatalf("failed to decode the response %q: %v", body, err)
	}

	if len(rows) != 1 {
		t.Errorf("got %d own profile rows, want 1: %s", len(rows), body)
	}
}

// TestGoTrue_AdminCreatedUser checks a user created through the admin API gets
// a profile from api.handle_new_user (named after the username metadata neutree
// passes) and can log in.
func TestGoTrue_AdminCreatedUser(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := createGoTrueTestUser(t, "gotrue-admin-created", "testpassword")

	var name string

	err := db.QueryRowContext(ctx, `SELECT (metadata).name FROM api.user_profiles WHERE id = $1`, user.ID).Scan(&name)
	if err != nil {
		t.Fatalf("no user profile for admin-created user %s: %v", user.ID, err)
	}

	if name != "gotrue-admin-created" {
		t.Errorf("profile name = %q, want %q", name, "gotrue-admin-created")
	}

	gotruePasswordLogin(t, user.Email, "testpassword")
}

// TestGoTrue_CustomProvidersAdminAPI checks the custom OIDC/OAuth provider
// admin API that SSO configuration is built on.
func TestGoTrue_CustomProvidersAdminAPI(t *testing.T) {
	t.Run("service role can list", func(t *testing.T) {
		token, err := storage.CreateServiceToken(TestJWTSecret)
		if err != nil {
			t.Fatalf("failed to mint a service token: %v", err)
		}

		code, body := gotrueRequest(t, http.MethodGet, "/admin/custom-providers", *token, nil)
		if code != http.StatusOK {
			t.Fatalf("GET /admin/custom-providers = HTTP %d, want 200: %s", code, body)
		}

		var resp map[string]json.RawMessage
		if err := json.Unmarshal(body, &resp); err != nil {
			t.Fatalf("failed to decode the response %q: %v", body, err)
		}

		var providers []json.RawMessage
		if err := json.Unmarshal(resp["providers"], &providers); err != nil {
			t.Errorf("response has no providers list: %s", body)
		}
	})

	t.Run("anonymous is rejected", func(t *testing.T) {
		code, body := gotrueRequest(t, http.MethodGet, "/admin/custom-providers", "", nil)
		if code != http.StatusUnauthorized {
			t.Errorf("GET /admin/custom-providers without a token = HTTP %d, want 401: %s", code, body)
		}
	})
}
