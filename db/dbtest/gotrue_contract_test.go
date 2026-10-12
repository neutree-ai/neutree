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

// The tests below pin the GoTrue behaviour neutree's external logins (LDAP,
// OIDC) build on. neutree authenticates the user itself, creates a GoTrue user
// without a password through the admin API, then mints a session for it with
// an admin magic link that it verifies at once. Deactivating a user bans it in
// GoTrue and ends its sessions by deleting auth.sessions rows.

// gotrueServiceToken mints a service_role token for the admin API.
func gotrueServiceToken(t *testing.T) string {
	t.Helper()

	token, err := storage.CreateServiceToken(TestJWTSecret)
	if err != nil {
		t.Fatalf("failed to mint a service token: %v", err)
	}

	return *token
}

// deleteGoTrueUserOnCleanup deletes the GoTrue user id when the test ends.
func deleteGoTrueUserOnCleanup(t *testing.T, id string) {
	t.Helper()

	t.Cleanup(func() {
		code, body := gotrueRequest(t, http.MethodDelete, "/admin/users/"+id, gotrueServiceToken(t), nil)
		if code != http.StatusOK {
			t.Errorf("failed to delete test user %s: HTTP %d: %s", id, code, body)
		}
	})
}

// gotrueTestEmail returns an email unique to this run.
func gotrueTestEmail(name string) string {
	return fmt.Sprintf("%s-%d@gotrue.test.local", name, time.Now().UnixNano())
}

type gotrueUser struct {
	ID               string         `json:"id"`
	Email            string         `json:"email"`
	EmailConfirmedAt *string        `json:"email_confirmed_at"`
	UserMetadata     map[string]any `json:"user_metadata"`
	AppMetadata      map[string]any `json:"app_metadata"`
}

// createExternalGoTrueUser creates a user the way an external login does: a
// confirmed email, no password, profile in user_metadata and the identity
// source in app_metadata.
func createExternalGoTrueUser(t *testing.T, name string) gotrueUser {
	t.Helper()

	email := gotrueTestEmail(name)

	code, body := gotrueRequest(t, http.MethodPost, "/admin/users", gotrueServiceToken(t), map[string]any{
		"email":         email,
		"email_confirm": true,
		"user_metadata": map[string]any{"preferred_username": name, "name": "Test " + name, "email": email},
		"app_metadata":  map[string]any{"identity_source": "contract-test"},
	})
	if code != http.StatusOK {
		t.Fatalf("POST /admin/users = HTTP %d, want 200: %s", code, body)
	}

	var user gotrueUser
	if err := json.Unmarshal(body, &user); err != nil {
		t.Fatalf("failed to decode the user %q: %v", body, err)
	}

	if user.ID == "" {
		t.Fatalf("POST /admin/users returned no user id: %s", body)
	}

	deleteGoTrueUserOnCleanup(t, user.ID)

	return user
}

type gotrueMagicLink struct {
	ID          string `json:"id"`
	HashedToken string `json:"hashed_token"`
}

// generateMagicLink calls the admin generate_link API for a magic link to
// email and returns the status code and the decoded link.
func generateMagicLink(t *testing.T, email string) (int, gotrueMagicLink, []byte) {
	t.Helper()

	code, body := gotrueRequest(t, http.MethodPost, "/admin/generate_link", gotrueServiceToken(t),
		map[string]string{"type": "magiclink", "email": email})

	var link gotrueMagicLink

	if code == http.StatusOK {
		if err := json.Unmarshal(body, &link); err != nil {
			t.Fatalf("failed to decode the generate_link response %q: %v", body, err)
		}
	}

	return code, link, body
}

type gotrueVerifiedSession struct {
	AccessToken  string     `json:"access_token"`
	RefreshToken string     `json:"refresh_token"`
	User         gotrueUser `json:"user"`
}

// verifyMagicLink exchanges a hashed magic link token on /verify, without a
// token, as neutree does.
func verifyMagicLink(t *testing.T, hashedToken string) (int, []byte) {
	t.Helper()

	return gotrueRequest(t, http.MethodPost, "/verify", "",
		map[string]string{"type": "magiclink", "token_hash": hashedToken})
}

// issueSession mints a session for user through generate_link and /verify
// and fails the test unless both succeed.
func issueSession(t *testing.T, user gotrueUser) gotrueVerifiedSession {
	t.Helper()

	code, link, body := generateMagicLink(t, user.Email)
	if code != http.StatusOK {
		t.Fatalf("generate_link = HTTP %d, want 200: %s", code, body)
	}

	code, body = verifyMagicLink(t, link.HashedToken)
	if code != http.StatusOK {
		t.Fatalf("POST /verify = HTTP %d, want 200: %s", code, body)
	}

	var session gotrueVerifiedSession
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("failed to decode the session %q: %v", body, err)
	}

	return session
}

// sessionIDClaim returns the session_id claim of a GoTrue access token.
func sessionIDClaim(t *testing.T, accessToken string) string {
	t.Helper()

	claims := jwt.MapClaims{}

	if _, err := jwt.ParseWithClaims(accessToken, claims, func(*jwt.Token) (any, error) {
		return []byte(TestJWTSecret), nil
	}); err != nil {
		t.Fatalf("access token does not verify: %v", err)
	}

	id, _ := claims["session_id"].(string)
	if id == "" {
		t.Fatalf("access token has no session_id claim")
	}

	return id
}

// refreshSession runs the refresh token grant and returns its status code.
func refreshSession(t *testing.T, refreshToken string) (int, []byte) {
	t.Helper()

	return gotrueRequest(t, http.MethodPost, "/token?grant_type=refresh_token", "",
		map[string]string{"refresh_token": refreshToken})
}

// setBan sets ban_duration on a user through the admin API ("none" lifts it).
func setBan(t *testing.T, userID, duration string) {
	t.Helper()

	code, body := gotrueRequest(t, http.MethodPut, "/admin/users/"+userID, gotrueServiceToken(t),
		map[string]string{"ban_duration": duration})
	if code != http.StatusOK {
		t.Fatalf("PUT /admin/users/%s ban_duration=%s = HTTP %d, want 200: %s", userID, duration, code, body)
	}
}

// TestGoTrue_AdminCreatedExternalUser checks a user created for an external
// login, with no password, keeps the fields neutree sets.
func TestGoTrue_AdminCreatedExternalUser(t *testing.T) {
	user := createExternalGoTrueUser(t, "gotrue-external")

	code, body := gotrueRequest(t, http.MethodGet, "/admin/users/"+user.ID, gotrueServiceToken(t), nil)
	if code != http.StatusOK {
		t.Fatalf("GET /admin/users/%s = HTTP %d, want 200: %s", user.ID, code, body)
	}

	var got gotrueUser
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("failed to decode the user %q: %v", body, err)
	}

	if got.EmailConfirmedAt == nil {
		t.Error("email_confirm=true did not confirm the email")
	}

	if got.UserMetadata["preferred_username"] != "gotrue-external" {
		t.Errorf("user_metadata = %v, want preferred_username gotrue-external", got.UserMetadata)
	}

	if got.AppMetadata["identity_source"] != "contract-test" {
		t.Errorf("app_metadata = %v, want identity_source contract-test", got.AppMetadata)
	}
}

// TestGoTrue_MagicLinkSession checks the admin generate_link + /verify flow
// that turns an external login into a GoTrue session.
func TestGoTrue_MagicLinkSession(t *testing.T) {
	db := GetTestDB(t)
	user := createExternalGoTrueUser(t, "gotrue-magiclink")

	code, link, body := generateMagicLink(t, user.Email)
	if code != http.StatusOK {
		t.Fatalf("generate_link = HTTP %d, want 200: %s", code, body)
	}

	if link.HashedToken == "" {
		t.Fatalf("generate_link returned no hashed_token: %s", body)
	}

	if link.ID != user.ID {
		t.Fatalf("generate_link user id = %q, want %q", link.ID, user.ID)
	}

	code, body = verifyMagicLink(t, link.HashedToken)
	if code != http.StatusOK {
		t.Fatalf("POST /verify = HTTP %d, want 200: %s", code, body)
	}

	var session gotrueVerifiedSession
	if err := json.Unmarshal(body, &session); err != nil {
		t.Fatalf("failed to decode the session %q: %v", body, err)
	}

	if session.AccessToken == "" || session.RefreshToken == "" {
		t.Fatalf("POST /verify returned an incomplete session: %s", body)
	}

	if session.User.ID != user.ID {
		t.Errorf("session user id = %q, want %q", session.User.ID, user.ID)
	}

	sessionID := sessionIDClaim(t, session.AccessToken)

	var owner string
	if err := db.QueryRow(`SELECT user_id::text FROM auth.sessions WHERE id = $1`, sessionID).Scan(&owner); err != nil {
		t.Fatalf("session_id claim %s has no auth.sessions row: %v", sessionID, err)
	}

	if owner != user.ID {
		t.Errorf("auth.sessions.user_id = %q, want %q", owner, user.ID)
	}

	t.Run("token is single use", func(t *testing.T) {
		if code, body := verifyMagicLink(t, link.HashedToken); code == http.StatusOK {
			t.Errorf("second POST /verify with the same token = HTTP 200, want an error: %s", body)
		}
	})

	t.Run("refresh token grant", func(t *testing.T) {
		if code, body := refreshSession(t, session.RefreshToken); code != http.StatusOK {
			t.Errorf("refresh = HTTP %d, want 200: %s", code, body)
		}
	})
}

// TestGoTrue_GenerateLinkSignsUpUnknownEmail pins that a magic link for an
// unknown email creates a user. neutree guards against this by comparing the
// returned user id with the user it asked for.
func TestGoTrue_GenerateLinkSignsUpUnknownEmail(t *testing.T) {
	db := GetTestDB(t)
	email := gotrueTestEmail("gotrue-unknown")

	code, link, body := generateMagicLink(t, email)
	if code != http.StatusOK {
		t.Fatalf("generate_link for an unknown email = HTTP %d, want 200: %s", code, body)
	}

	if link.ID == "" {
		t.Fatalf("generate_link for an unknown email returned no user id: %s", body)
	}

	deleteGoTrueUserOnCleanup(t, link.ID)

	var gotEmail string
	if err := db.QueryRow(`SELECT email FROM auth.users WHERE id = $1`, link.ID).Scan(&gotEmail); err != nil {
		t.Fatalf("generate_link did not create user %s: %v", link.ID, err)
	}

	if gotEmail != email {
		t.Errorf("created user email = %q, want %q", gotEmail, email)
	}

	var name string
	if err := db.QueryRow(`SELECT (metadata).name FROM api.user_profiles WHERE id = $1`, link.ID).Scan(&name); err != nil {
		t.Errorf("no profile for the signed-up user %s: %v", link.ID, err)
	}
}

// TestGoTrue_BannedUser checks a ban stops a user's sessions from refreshing
// and new ones from being issued, and that lifting it lets them be issued.
func TestGoTrue_BannedUser(t *testing.T) {
	db := GetTestDB(t)
	user := createExternalGoTrueUser(t, "gotrue-banned")
	session := issueSession(t, user)

	// identitysync bans a deactivated user for 100 years.
	setBan(t, user.ID, "876000h")

	var banned bool
	if err := db.QueryRow(`SELECT banned_until > now() FROM auth.users WHERE id = $1`, user.ID).Scan(&banned); err != nil {
		t.Fatalf("failed to read auth.users.banned_until: %v", err)
	}

	if !banned {
		t.Error("ban_duration did not set banned_until in the future")
	}

	t.Run("refresh is rejected", func(t *testing.T) {
		if code, body := refreshSession(t, session.RefreshToken); code == http.StatusOK {
			t.Errorf("refresh of a banned user = HTTP 200, want an error: %s", body)
		}
	})

	t.Run("verify is rejected", func(t *testing.T) {
		code, link, body := generateMagicLink(t, user.Email)
		if code != http.StatusOK {
			// GoTrue refusing the link is as good as refusing the session.
			return
		}

		if code, body = verifyMagicLink(t, link.HashedToken); code == http.StatusOK {
			t.Errorf("POST /verify for a banned user = HTTP 200, want an error: %s", body)
		}
	})

	t.Run("lifting the ban allows a session", func(t *testing.T) {
		setBan(t, user.ID, "none")
		issueSession(t, user)
	})
}

// TestGoTrue_SessionTables checks the auth.sessions and auth.refresh_tokens
// columns neutree reads and writes directly, and that deleting a session row
// ends the session.
func TestGoTrue_SessionTables(t *testing.T) {
	db := GetTestDB(t)

	want := map[string][]string{
		"sessions":       {"id", "user_id", "not_after"},
		"refresh_tokens": {"token", "user_id", "session_id", "revoked"},
		"users":          {"id", "email", "banned_until", "deleted_at"},
	}

	for table, columns := range want {
		for _, column := range columns {
			var exists bool

			err := db.QueryRow(`SELECT EXISTS(
				SELECT 1 FROM information_schema.columns
				WHERE table_schema = 'auth' AND table_name = $1 AND column_name = $2)`, table, column).Scan(&exists)
			if err != nil {
				t.Fatalf("failed to read information_schema: %v", err)
			}

			if !exists {
				t.Errorf("auth.%s has no %s column", table, column)
			}
		}
	}

	user := createExternalGoTrueUser(t, "gotrue-session-delete")
	session := issueSession(t, user)
	sessionID := sessionIDClaim(t, session.AccessToken)

	// refresh_tokens.user_id is text, unlike sessions.user_id.
	var tokens int
	if err := db.QueryRow(`SELECT count(*) FROM auth.refresh_tokens WHERE user_id = $1 AND session_id = $2`,
		user.ID, sessionID).Scan(&tokens); err != nil {
		t.Fatalf("failed to read auth.refresh_tokens: %v", err)
	}

	if tokens == 0 {
		t.Errorf("no auth.refresh_tokens row for session %s", sessionID)
	}

	if _, err := db.Exec(`DELETE FROM auth.sessions WHERE id = $1`, sessionID); err != nil {
		t.Fatalf("failed to delete auth.sessions row: %v", err)
	}

	if code, body := refreshSession(t, session.RefreshToken); code == http.StatusOK {
		t.Errorf("refresh after deleting the session = HTTP 200, want an error: %s", body)
	}
}
