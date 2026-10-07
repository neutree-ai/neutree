package dbtest

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/lib/pq"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// These tests cover api.identity_sources (migrations 104/105): RLS by the
// identity_source:* permissions, validation and defaults in the write trigger,
// write-only secrets encrypted into api.identity_source_secrets, and the two
// SECURITY DEFINER functions (the public login list and the service-role
// secret read). They go through PostgREST, which is what neutree-api writes
// through and which replaces a composite column as a whole on PATCH.

const seededAdminEmail = "admin@neutree.local"

// seededAdminPassword is set by PGOPTIONS in db/docker-compose.test.yml.
const seededAdminPassword = "neutree-test"

// postgrestAs sends a request to the test PostgREST with the given bearer token
// (none when empty, i.e. the anonymous role).
func postgrestAs(t *testing.T, token, method, path, body string) (int, string) {
	t.Helper()

	req, err := http.NewRequest(method, GetPostgRESTURL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build the request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Profile", "api")
	req.Header.Set("Content-Profile", "api")
	req.Header.Set("Prefer", "return=representation")

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

	return resp.StatusCode, string(raw)
}

func adminToken(t *testing.T) string {
	t.Helper()
	waitForPostgREST(t)

	return gotruePasswordLogin(t, seededAdminEmail, seededAdminPassword).AccessToken
}

// userTokenWithPermissions creates a user holding a global role with exactly
// the given permissions (none for an empty list) and logs it in.
func userTokenWithPermissions(t *testing.T, prefix string, permissions []string) string {
	t.Helper()
	waitForPostgREST(t)

	db := GetTestDB(t)
	ctx := context.Background()
	username := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	password := "password123"

	if len(permissions) == 0 {
		user := createGoTrueTestUser(t, username, password)
		return gotruePasswordLogin(t, user.Email, password).AccessToken
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin failed: %v", err)
	}

	email := username + "@identity-source.test.local"
	createUserWithPermissions(t, tx, username, email, permissions)

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit failed: %v", err)
	}

	return gotruePasswordLogin(t, email, password).AccessToken
}

// newIdentitySourceName returns a unique valid name: at most 32 characters.
func newIdentitySourceName(prefix string) string {
	return fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano()%1_000_000_000_000)
}

func ldapSourceJSON(name, displayName, bindPassword string, enabled bool) string {
	ldap := map[string]any{
		"url":          "ldaps://ldap.example.org:636",
		"bind_dn":      "cn=svc,dc=example,dc=org",
		"user_base_dn": "ou=people,dc=example,dc=org",
		"user_filter":  "(&(objectClass=inetOrgPerson)(uid={username}))",
	}
	if bindPassword != "" {
		ldap["bind_password"] = bindPassword
	}

	return mustJSON(map[string]any{
		"api_version": "v1",
		"kind":        "IdentitySource",
		"metadata":    map[string]any{"name": name, "display_name": displayName},
		"spec":        map[string]any{"type": "ldap", "enabled": enabled, "ldap": ldap},
	})
}

func oidcSpecJSON(clientSecret string) map[string]any {
	oidc := map[string]any{
		"issuer":            "https://idp.example.org/realms/neutree",
		"client_id":         "neutree",
		"redirect_url":      "https://neutree.example.org/api/v1/auth/oidc/callback",
		"allowed_redirects": []string{"https://neutree.example.org/"},
	}
	if clientSecret != "" {
		oidc["client_secret"] = clientSecret
	}

	return oidc
}

func mustJSON(v any) string {
	raw, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}

	return string(raw)
}

// createIdentitySource creates a source as admin and removes it (and so its
// secrets) when the test ends.
func createIdentitySource(t *testing.T, token, body string) v1.IdentitySource {
	t.Helper()

	code, raw := postgrestAs(t, token, http.MethodPost, "/identity_sources", body)
	if code != http.StatusCreated {
		t.Fatalf("POST /identity_sources = HTTP %d, want 201: %s", code, raw)
	}

	var rows []v1.IdentitySource
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("failed to decode the created identity source %q: %v", raw, err)
	}

	id := rows[0].ID

	t.Cleanup(func() {
		if _, err := GetTestDB(t).Exec(`DELETE FROM api.identity_sources WHERE id = $1`, id); err != nil {
			t.Errorf("failed to delete identity source %d: %v", id, err)
		}
	})

	return rows[0]
}

func storedSecrets(t *testing.T, name string) *storage.IdentitySourceSecrets {
	t.Helper()

	secrets, err := NewTestStorage(t).GetIdentitySourceSecrets(name)
	if err != nil {
		t.Fatalf("GetIdentitySourceSecrets(%s) failed: %v", name, err)
	}

	return secrets
}

func assertNoSecretInBody(t *testing.T, body string, secrets ...string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(body, secret) {
			t.Errorf("response carries the secret %q: %s", secret, body)
		}
	}
}

func TestIdentitySource_AdminCRUD(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("crud")
	secret := "bind-secret-" + name

	created := createIdentitySource(t, token, ldapSourceJSON(name, "Corp LDAP", secret, true))
	path := fmt.Sprintf("/identity_sources?id=eq.%d", created.ID)

	t.Run("create applies defaults and hides the secret", func(t *testing.T) {
		ldap := created.Spec.LDAP
		if ldap == nil || ldap.BindPassword != "" {
			t.Fatalf("created spec.ldap = %+v, want it without bind_password", ldap)
		}

		if ldap.Timeout != v1.DefaultIdentitySourceLDAPTimeoutSeconds || ldap.Attributes == nil ||
			ldap.Attributes.ID != "entryUUID" || ldap.Attributes.Username != "uid" ||
			ldap.Attributes.Email != "mail" || ldap.Attributes.DisplayName != "cn" || ldap.Attributes.MemberOf != "" {
			t.Errorf("defaults not applied: %+v / %+v", ldap, ldap.Attributes)
		}

		if created.Metadata.DisplayName != "Corp LDAP" || created.Metadata.Workspace != "" {
			t.Errorf("metadata = %+v", created.Metadata)
		}

		if got := storedSecrets(t, name).LDAPBindPassword; got != secret {
			t.Errorf("stored bind password = %q, want %q", got, secret)
		}
	})

	t.Run("read never returns the secret", func(t *testing.T) {
		code, raw := postgrestAs(t, token, http.MethodGet, path, "")
		if code != http.StatusOK || !strings.Contains(raw, name) {
			t.Fatalf("GET = HTTP %d: %s", code, raw)
		}

		assertNoSecretInBody(t, raw, secret)
	})

	t.Run("update", func(t *testing.T) {
		body := ldapSourceJSON(name, "Renamed", "", false)
		if code, raw := postgrestAs(t, token, http.MethodPatch, path, body); code != http.StatusOK {
			t.Fatalf("PATCH = HTTP %d: %s", code, raw)
		}

		code, raw := postgrestAs(t, token, http.MethodGet, path, "")
		if code != http.StatusOK || !strings.Contains(raw, `"display_name":"Renamed"`) || !strings.Contains(raw, `"enabled":false`) {
			t.Errorf("GET after update = HTTP %d: %s", code, raw)
		}
	})

	t.Run("name is immutable", func(t *testing.T) {
		body := ldapSourceJSON(name+"-x", "Renamed", "", false)

		code, raw := postgrestAs(t, token, http.MethodPatch, path, body)
		if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10251"`) {
			t.Errorf("rename = HTTP %d, want 400 with code 10251: %s", code, raw)
		}
	})

	t.Run("soft delete hides it from login and secret reads", func(t *testing.T) {
		enable := ldapSourceJSON(name, "Renamed", "", true)
		if code, raw := postgrestAs(t, token, http.MethodPatch, path, enable); code != http.StatusOK {
			t.Fatalf("PATCH = HTTP %d: %s", code, raw)
		}

		if !loginListHas(t, name) {
			t.Fatalf("enabled source %s is not on the login list", name)
		}

		body := mustJSON(map[string]any{"metadata": map[string]any{
			"name": name, "display_name": "Renamed", "deletion_timestamp": time.Now().UTC().Format(time.RFC3339),
		}})
		if code, raw := postgrestAs(t, token, http.MethodPatch, path, body); code != http.StatusOK {
			t.Fatalf("soft delete = HTTP %d: %s", code, raw)
		}

		if loginListHas(t, name) {
			t.Errorf("soft-deleted source %s is still on the login list", name)
		}

		if _, err := NewTestStorage(t).GetIdentitySourceSecrets(name); !errors.Is(err, storage.ErrResourceNotFound) {
			t.Errorf("secrets of a soft-deleted source = %v, want ErrResourceNotFound", err)
		}
	})

	t.Run("hard delete removes the secrets", func(t *testing.T) {
		if code, raw := postgrestAs(t, token, http.MethodDelete, path, ""); code != http.StatusOK {
			t.Fatalf("DELETE = HTTP %d: %s", code, raw)
		}

		var count int
		if err := GetTestDB(t).QueryRow(`SELECT count(*) FROM api.identity_source_secrets WHERE identity_source_id = $1`,
			created.ID).Scan(&count); err != nil || count != 0 {
			t.Errorf("secret rows left = %d (%v), want 0", count, err)
		}
	})
}

func TestIdentitySource_Permissions(t *testing.T) {
	admin := adminToken(t)
	name := newIdentitySourceName("perm")
	secret := "bind-secret-" + name
	source := createIdentitySource(t, admin, ldapSourceJSON(name, "", secret, true))
	path := fmt.Sprintf("/identity_sources?id=eq.%d", source.ID)

	t.Run("user without the permission sees and changes nothing", func(t *testing.T) {
		token := userTokenWithPermissions(t, "is-none", nil)

		if code, raw := postgrestAs(t, token, http.MethodGet, path, ""); code != http.StatusOK || raw != "[]" {
			t.Errorf("GET = HTTP %d %s, want 200 []", code, raw)
		}

		if code, raw := postgrestAs(t, token, http.MethodPost, "/identity_sources",
			ldapSourceJSON(newIdentitySourceName("perm-no"), "", "pw", true)); code != http.StatusForbidden {
			t.Errorf("POST = HTTP %d, want 403: %s", code, raw)
		}

		if code, raw := postgrestAs(t, token, http.MethodPatch, path, ldapSourceJSON(name, "Hijacked", "", false)); code != http.StatusOK || raw != "[]" {
			t.Errorf("PATCH = HTTP %d %s, want 200 [] (no row visible)", code, raw)
		}

		if code, raw := postgrestAs(t, token, http.MethodDelete, path, ""); code != http.StatusOK || raw != "[]" {
			t.Errorf("DELETE = HTTP %d %s, want 200 [] (no row visible)", code, raw)
		}
	})

	t.Run("a non-admin role with identity_source:read can read, not write", func(t *testing.T) {
		token := userTokenWithPermissions(t, "is-read", []string{"identity_source:read"})

		code, raw := postgrestAs(t, token, http.MethodGet, path, "")
		if code != http.StatusOK || !strings.Contains(raw, name) {
			t.Fatalf("GET = HTTP %d, want the source: %s", code, raw)
		}

		assertNoSecretInBody(t, raw, secret)

		if code, raw := postgrestAs(t, token, http.MethodPatch, path, ldapSourceJSON(name, "Hijacked", "", false)); code != http.StatusForbidden {
			t.Errorf("PATCH = HTTP %d, want 403: %s", code, raw)
		}

		if code, raw := postgrestAs(t, token, http.MethodPost, "/identity_sources",
			ldapSourceJSON(newIdentitySourceName("perm-ro"), "", "pw", true)); code != http.StatusForbidden {
			t.Errorf("POST = HTTP %d, want 403: %s", code, raw)
		}
	})

	t.Run("a non-admin role with the full item set can manage", func(t *testing.T) {
		token := userTokenWithPermissions(t, "is-mgr", []string{
			"identity_source:read", "identity_source:create", "identity_source:update", "identity_source:delete",
		})

		own := createIdentitySource(t, token, ldapSourceJSON(newIdentitySourceName("perm-mgr"), "", "pw", true))

		if code, raw := postgrestAs(t, token, http.MethodPatch, fmt.Sprintf("/identity_sources?id=eq.%d", own.ID),
			ldapSourceJSON(own.Metadata.Name, "Managed", "", true)); code != http.StatusOK || !strings.Contains(raw, "Managed") {
			t.Errorf("PATCH = HTTP %d: %s", code, raw)
		}
	})

	t.Run("users cannot read the secrets table or function", func(t *testing.T) {
		token := userTokenWithPermissions(t, "is-sec", []string{"identity_source:read"})

		if code, raw := postgrestAs(t, token, http.MethodGet, "/identity_source_secrets", ""); code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("GET /identity_source_secrets = HTTP %d, want 401/403: %s", code, raw)
		}

		code, raw := postgrestAs(t, token, http.MethodPost, "/rpc/get_identity_source_secrets", mustJSON(map[string]string{"p_name": name}))
		if code != http.StatusForbidden && code != http.StatusUnauthorized {
			t.Errorf("POST /rpc/get_identity_source_secrets = HTTP %d, want 401/403: %s", code, raw)
		}

		assertNoSecretInBody(t, raw, secret)

		db := GetTestDB(t)
		ctx := context.Background()

		for _, role := range []string{"api_user", "anonymous"} {
			for _, query := range []string{
				`SELECT 1 FROM api.identity_source_secrets`,
				fmt.Sprintf(`SELECT * FROM api.get_identity_source_secrets('%s')`, name),
			} {
				tx, err := db.BeginTx(ctx, nil)
				if err != nil {
					t.Fatalf("begin failed: %v", err)
				}

				if _, err := tx.ExecContext(ctx, "SET LOCAL ROLE "+role); err != nil {
					t.Fatalf("set role failed: %v", err)
				}

				_, err = tx.ExecContext(ctx, query)
				_ = tx.Rollback()

				var pqErr *pq.Error
				if !errors.As(err, &pqErr) || pqErr.Code != "42501" {
					t.Errorf("%s as %s = %v, want insufficient_privilege", query, role, err)
				}
			}
		}
	})
}

func TestIdentitySource_SecretsEncryptedAtRest(t *testing.T) {
	token := adminToken(t)
	secret := fmt.Sprintf("same-secret-%d", time.Now().UnixNano())

	a := createIdentitySource(t, token, ldapSourceJSON(newIdentitySourceName("enc-a"), "", secret, true))
	b := createIdentitySource(t, token, ldapSourceJSON(newIdentitySourceName("enc-b"), "", secret, true))

	db := GetTestDB(t)

	var cipherA, cipherB []byte
	if err := db.QueryRow(`SELECT bind_password FROM api.identity_source_secrets WHERE identity_source_id = $1`, a.ID).Scan(&cipherA); err != nil {
		t.Fatalf("read ciphertext a: %v", err)
	}

	if err := db.QueryRow(`SELECT bind_password FROM api.identity_source_secrets WHERE identity_source_id = $1`, b.ID).Scan(&cipherB); err != nil {
		t.Fatalf("read ciphertext b: %v", err)
	}

	if len(cipherA) == 0 || bytes.Contains(cipherA, []byte(secret)) {
		t.Errorf("stored bind_password is empty or holds the plaintext: %x", cipherA)
	}

	if bytes.Equal(cipherA, cipherB) {
		t.Error("two equal secrets have the same ciphertext; the encryption is deterministic")
	}

	var plaintextInSpec int
	if err := db.QueryRow(`SELECT count(*) FROM api.identity_sources
		WHERE id IN ($1, $2) AND ((spec).ldap).bind_password IS NOT NULL`, a.ID, b.ID).Scan(&plaintextInSpec); err != nil || plaintextInSpec != 0 {
		t.Errorf("rows with a bind_password in spec = %d (%v), want 0", plaintextInSpec, err)
	}

	var rowText string
	if err := db.QueryRow(`SELECT t::text FROM api.identity_sources t WHERE id = $1`, a.ID).Scan(&rowText); err != nil || strings.Contains(rowText, secret) {
		t.Errorf("identity_sources row holds the secret (%v): %s", err, rowText)
	}

	for _, source := range []v1.IdentitySource{a, b} {
		if got := storedSecrets(t, source.Metadata.Name).LDAPBindPassword; got != secret {
			t.Errorf("decrypted bind password of %s = %q, want %q", source.Metadata.Name, got, secret)
		}
	}

	t.Run("a write without the encryption key is refused", func(t *testing.T) {
		_, err := db.Exec(`INSERT INTO api.identity_sources (api_version, kind, metadata, spec) VALUES ('v1', 'IdentitySource',
			ROW($1, NULL, NULL, NULL, NULL, NULL, '{}', '{}')::api.metadata,
			ROW('ldap', true, ROW('ldap://h', NULL, NULL, NULL, NULL, 'cn=svc', 'pw', 'ou=p', '(uid={username})', NULL)::api.identity_source_ldap_spec, NULL)::api.identity_source_spec)`,
			newIdentitySourceName("enc-nokey"))
		if err == nil || !strings.Contains(err.Error(), "10259") {
			t.Errorf("insert without app.settings.jwt_secret = %v, want code 10259", err)
		}
	})
}

func TestIdentitySource_PartialUpdateKeepsSecret(t *testing.T) {
	token := adminToken(t)
	name := newIdentitySourceName("keep")
	secret := "bind-secret-" + name
	source := createIdentitySource(t, token, ldapSourceJSON(name, "", secret, true))
	path := fmt.Sprintf("/identity_sources?id=eq.%d", source.ID)

	patch := func(t *testing.T, body string) {
		t.Helper()

		if code, raw := postgrestAs(t, token, http.MethodPatch, path, body); code != http.StatusOK {
			t.Fatalf("PATCH = HTTP %d: %s", code, raw)
		}
	}

	t.Run("spec without bind_password keeps it", func(t *testing.T) {
		patch(t, ldapSourceJSON(name, "", "", true))

		if got := storedSecrets(t, name).LDAPBindPassword; got != secret {
			t.Errorf("bind password = %q, want %q", got, secret)
		}
	})

	t.Run("empty bind_password keeps it", func(t *testing.T) {
		body := strings.Replace(ldapSourceJSON(name, "", "", true), `"bind_dn"`, `"bind_password":"","bind_dn"`, 1)
		patch(t, body)

		if got := storedSecrets(t, name).LDAPBindPassword; got != secret {
			t.Errorf("bind password = %q, want %q", got, secret)
		}
	})

	t.Run("status-only write keeps it", func(t *testing.T) {
		patch(t, `{"status":{"phase":"Connected","error_message":""}}`)

		if got := storedSecrets(t, name).LDAPBindPassword; got != secret {
			t.Errorf("bind password = %q, want %q", got, secret)
		}
	})

	t.Run("a new bind_password replaces it", func(t *testing.T) {
		patch(t, ldapSourceJSON(name, "", "rotated", true))

		if got := storedSecrets(t, name).LDAPBindPassword; got != "rotated" {
			t.Errorf("bind password = %q, want rotated", got)
		}
	})

	// The resource proxy's backfill turns a type switch into a PATCH that still
	// carries an ldap object with only a null bind_password.
	t.Run("switching to oidc drops the ldap secret", func(t *testing.T) {
		patch(t, mustJSON(map[string]any{"spec": map[string]any{
			"type": "oidc", "enabled": true,
			"ldap": map[string]any{"bind_password": nil},
			"oidc": oidcSpecJSON("client-secret-" + name),
		}}))

		secrets := storedSecrets(t, name)
		if secrets.LDAPBindPassword != "" || secrets.OIDCClientSecret != "client-secret-"+name {
			t.Errorf("secrets after the switch = %+v", secrets)
		}

		code, raw := postgrestAs(t, token, http.MethodGet, path, "")
		if code != http.StatusOK || !strings.Contains(raw, `"ldap":null`) || !strings.Contains(raw, `"scopes":["openid","profile","email"]`) {
			t.Errorf("GET after the switch = HTTP %d: %s", code, raw)
		}

		assertNoSecretInBody(t, raw, "client-secret-"+name)
	})

	t.Run("switching back to ldap needs a bind_password", func(t *testing.T) {
		code, raw := postgrestAs(t, token, http.MethodPatch, path, ldapSourceJSON(name, "", "", true))
		if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10255"`) || !strings.Contains(raw, "bind_password") {
			t.Errorf("PATCH = HTTP %d, want 400 with code 10255 for bind_password: %s", code, raw)
		}
	})
}

func TestIdentitySource_Validation(t *testing.T) {
	token := adminToken(t)

	ldapSpec := func(mutate func(map[string]any)) map[string]any {
		ldap := map[string]any{
			"url": "ldap://ldap.example.org", "bind_dn": "cn=svc", "bind_password": "pw",
			"user_base_dn": "ou=people", "user_filter": "(uid={username})",
		}
		mutate(ldap)

		return map[string]any{"type": "ldap", "enabled": true, "ldap": ldap}
	}
	oidcSpec := func(mutate func(map[string]any)) map[string]any {
		oidc := oidcSpecJSON("")
		mutate(oidc)

		return map[string]any{"type": "oidc", "enabled": true, "oidc": oidc}
	}

	tests := []struct {
		name     string
		metadata map[string]any
		spec     any
		wantCode string
	}{
		{"name with a dot", map[string]any{"name": "corp.ldap"}, ldapSpec(func(map[string]any) {}), "10250"},
		{"name too long", map[string]any{"name": strings.Repeat("a", 33)}, ldapSpec(func(map[string]any) {}), "10250"},
		{"workspace set", map[string]any{"workspace": "default"}, ldapSpec(func(map[string]any) {}), "10252"},
		{"no spec", nil, nil, "10253"},
		{"unknown type", nil, map[string]any{"type": "saml"}, "10253"},
		{"ldap without sub-object", nil, map[string]any{"type": "ldap"}, "10254"},
		{"ldap with oidc sub-object", nil, func() map[string]any {
			s := ldapSpec(func(map[string]any) {})
			s["oidc"] = map[string]any{"issuer": "https://x"}
			return s
		}(), "10254"},
		{"ldap missing user_base_dn", nil, ldapSpec(func(l map[string]any) { delete(l, "user_base_dn") }), "10255"},
		{"ldap missing bind_password", nil, ldapSpec(func(l map[string]any) { delete(l, "bind_password") }), "10255"},
		{"ldap http url", nil, ldapSpec(func(l map[string]any) { l["url"] = "http://ldap.example.org" }), "10256"},
		{"ldaps with start_tls", nil, ldapSpec(func(l map[string]any) {
			l["url"] = "ldaps://ldap.example.org"
			l["start_tls"] = true
		}), "10258"},
		{"negative timeout", nil, ldapSpec(func(l map[string]any) { l["timeout"] = -1 }), "10258"},
		{"filter without placeholder", nil, ldapSpec(func(l map[string]any) { l["user_filter"] = "(uid=*)" }), "10257"},
		{"ldap CA not PEM", nil, ldapSpec(func(l map[string]any) { l["ca_cert"] = "junk" }), "10258"},
		{"oidc missing client_id", nil, oidcSpec(func(o map[string]any) { delete(o, "client_id") }), "10255"},
		{"oidc without allowed_redirects", nil, oidcSpec(func(o map[string]any) { o["allowed_redirects"] = []string{} }), "10255"},
		{"oidc issuer not http", nil, oidcSpec(func(o map[string]any) { o["issuer"] = "ftp://idp" }), "10256"},
		{"oidc allowed redirect with query", nil, oidcSpec(func(o map[string]any) {
			o["allowed_redirects"] = []string{"https://neutree.example.org/?x=1"}
		}), "10256"},
		{"oidc scopes without openid", nil, oidcSpec(func(o map[string]any) { o["scopes"] = []string{"profile"} }), "10257"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			metadata := map[string]any{"name": newIdentitySourceName("val")}
			for k, v := range tt.metadata {
				metadata[k] = v
			}

			body := mustJSON(map[string]any{"api_version": "v1", "kind": "IdentitySource", "metadata": metadata, "spec": tt.spec})

			code, raw := postgrestAs(t, token, http.MethodPost, "/identity_sources", body)
			if code == http.StatusCreated {
				var rows []v1.IdentitySource
				if json.Unmarshal([]byte(raw), &rows) == nil && len(rows) == 1 {
					_, _ = GetTestDB(t).Exec(`DELETE FROM api.identity_sources WHERE id = $1`, rows[0].ID)
				}
			}

			if code != http.StatusBadRequest || !strings.Contains(raw, fmt.Sprintf(`"code":"%s"`, tt.wantCode)) {
				t.Errorf("POST = HTTP %d, want 400 with code %s: %s", code, tt.wantCode, raw)
			}
		})
	}
}

func loginListHas(t *testing.T, name string) bool {
	t.Helper()

	sources, err := NewTestStorage(t).ListLoginIdentitySources()
	if err != nil {
		t.Fatalf("ListLoginIdentitySources failed: %v", err)
	}

	for _, source := range sources {
		if source.Name == name {
			return true
		}
	}

	return false
}

func TestIdentitySource_PublicLoginList(t *testing.T) {
	token := adminToken(t)

	enabled := createIdentitySource(t, token, ldapSourceJSON(newIdentitySourceName("pub-on"), "Corp Login", "pw-on", true))
	disabled := createIdentitySource(t, token, ldapSourceJSON(newIdentitySourceName("pub-off"), "", "pw-off", false))

	oidcName := newIdentitySourceName("pub-oidc")
	createIdentitySource(t, token, mustJSON(map[string]any{
		"api_version": "v1", "kind": "IdentitySource",
		"metadata": map[string]any{"name": oidcName},
		"spec":     map[string]any{"type": "oidc", "enabled": true, "oidc": oidcSpecJSON("pw-oidc")},
	}))

	// Anonymous: no Authorization header, the way the login page calls it.
	code, raw := postgrestAs(t, "", http.MethodPost, "/rpc/list_login_identity_sources", "{}")
	if code != http.StatusOK {
		t.Fatalf("anonymous list = HTTP %d: %s", code, raw)
	}

	var rows []map[string]any
	if err := json.Unmarshal([]byte(raw), &rows); err != nil {
		t.Fatalf("failed to decode %q: %v", raw, err)
	}

	byName := map[string]map[string]any{}

	for _, row := range rows {
		keys := make([]string, 0, len(row))
		for k := range row {
			keys = append(keys, k)
		}

		sort.Strings(keys)

		if strings.Join(keys, ",") != "display_name,name,type" {
			t.Errorf("row has fields %v, want exactly name, display_name, type", keys)
		}

		name, _ := row["name"].(string)
		byName[name] = row
	}

	if got := byName[enabled.Metadata.Name]; got == nil || got["display_name"] != "Corp Login" || got["type"] != "ldap" {
		t.Errorf("enabled LDAP source = %v", got)
	}

	if got := byName[oidcName]; got == nil || got["display_name"] != oidcName || got["type"] != "oidc" {
		t.Errorf("enabled OIDC source (display name defaults to name) = %v", got)
	}

	if _, ok := byName[disabled.Metadata.Name]; ok {
		t.Errorf("disabled source %s is on the login list", disabled.Metadata.Name)
	}

	assertNoSecretInBody(t, raw, "pw-on", "pw-off", "pw-oidc", "ldap.example.org", "idp.example.org")

	t.Run("anonymous cannot read the table", func(t *testing.T) {
		if code, raw := postgrestAs(t, "", http.MethodGet, "/identity_sources", ""); code != http.StatusUnauthorized && code != http.StatusForbidden {
			t.Errorf("anonymous GET /identity_sources = HTTP %d, want 401/403: %s", code, raw)
		}
	})

	t.Run("service storage sees the same list", func(t *testing.T) {
		if !loginListHas(t, enabled.Metadata.Name) || loginListHas(t, disabled.Metadata.Name) {
			t.Error("storage login list disagrees with the anonymous one")
		}
	})
}
