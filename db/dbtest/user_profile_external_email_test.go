package dbtest

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"testing"
	"time"
)

// These tests cover migration 108: the profile email of a user from an
// identity source is read-only, except for the value the system sets itself,
// and every profile carries a read-only neutree.ai/identity-source label that
// tells external users apart.

const identitySourceLabel = "neutree.ai/identity-source"

type externalEmailUser struct {
	id        string
	directory string // the directory mail, "" for a local user
	email     string // the email the profile starts with
}

func createExternalEmailUser(t *testing.T, source, prefix string) externalEmailUser {
	t.Helper()

	run := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	directory := run + "@corp.test.local"
	id := createSSOShapedUser(t, map[string]any{
		"email":         run + "@" + source + ".neutree.local",
		"email_confirm": true,
		"app_metadata":  map[string]any{"identity_source": source},
		"user_metadata": map[string]any{"preferred_username": run, "email": directory},
	})

	return externalEmailUser{id: id, directory: directory, email: directory}
}

func createLocalEmailUser(t *testing.T, prefix string) externalEmailUser {
	t.Helper()

	run := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())
	email := run + "@corp.test.local"
	id := createSSOShapedUser(t, map[string]any{
		"email":         email,
		"password":      "Passw0rd-" + run,
		"email_confirm": true,
		"user_metadata": map[string]any{"username": run},
	})

	return externalEmailUser{id: id, email: email}
}

func userProfileLabels(t *testing.T, id string) map[string]string {
	t.Helper()

	var raw *string
	if err := GetTestDB(t).QueryRowContext(context.Background(),
		`SELECT (metadata).labels::text FROM api.user_profiles WHERE id = $1`, id).Scan(&raw); err != nil {
		t.Fatalf("no user profile for %s: %v", id, err)
	}

	labels := map[string]string{}
	if raw != nil {
		if err := json.Unmarshal([]byte(*raw), &labels); err != nil {
			t.Fatalf("labels of %s are not a string map: %s", id, *raw)
		}
	}

	return labels
}

// patchProfile PATCHes a user profile through PostgREST as token.
func patchProfile(t *testing.T, token, id string, body any) (int, string) {
	t.Helper()

	return postgrestAs(t, token, http.MethodPatch, "/user_profiles?id=eq."+id, mustJSON(body))
}

// profileMetadata returns the profile metadata as PostgREST serves it, to send
// back whole: a PATCH of a composite column replaces every field.
func profileMetadata(t *testing.T, token, id string) map[string]any {
	t.Helper()

	code, raw := postgrestAs(t, token, http.MethodGet, "/user_profiles?select=metadata&id=eq."+id, "")
	if code != http.StatusOK {
		t.Fatalf("GET profile %s = HTTP %d: %s", id, code, raw)
	}

	var rows []struct {
		Metadata map[string]any `json:"metadata"`
	}
	if err := json.Unmarshal([]byte(raw), &rows); err != nil || len(rows) != 1 {
		t.Fatalf("GET profile %s: %v: %s", id, err, raw)
	}

	return rows[0].Metadata
}

func TestUserProfileExternalEmail_ReadOnly(t *testing.T) {
	admin := adminToken(t)

	for _, source := range []string{"ldap", "oidc"} {
		t.Run(source+" user: admin cannot change the email", func(t *testing.T) {
			user := createExternalEmailUser(t, source, "ro-"+source)

			code, raw := patchProfile(t, admin, user.id, map[string]any{"spec": map[string]any{"email": "changed@corp.test.local"}})
			if code != http.StatusBadRequest || !strings.Contains(raw, `"code":"10260"`) {
				t.Fatalf("PATCH email = HTTP %d, want 400 with code 10260: %s", code, raw)
			}

			if got := userProfileEmail(t, user.id); got != user.directory {
				t.Errorf("spec.email = %q, want it unchanged %q", got, user.directory)
			}
		})
	}

	t.Run("external user: resending the same email passes", func(t *testing.T) {
		user := createExternalEmailUser(t, "ldap", "ro-same")

		code, raw := patchProfile(t, admin, user.id, map[string]any{"spec": map[string]any{"email": user.directory}})
		if code != http.StatusOK {
			t.Fatalf("same-value PATCH = HTTP %d, want 200: %s", code, raw)
		}
	})

	t.Run("external user: other fields can change and the label stays", func(t *testing.T) {
		user := createExternalEmailUser(t, "oidc", "ro-other")

		metadata := profileMetadata(t, admin, user.id)
		metadata["display_name"] = "Renamed by admin"
		metadata["labels"] = map[string]any{"team": "a"} // drops the identity source label
		metadata["annotations"] = map[string]any{"note": "x"}

		code, raw := patchProfile(t, admin, user.id, map[string]any{"metadata": metadata, "spec": map[string]any{"email": user.directory}})
		if code != http.StatusOK {
			t.Fatalf("metadata PATCH = HTTP %d, want 200: %s", code, raw)
		}

		if _, displayName := userProfileNames(t, user.id); displayName != "Renamed by admin" {
			t.Errorf("display_name = %q, want the new one", displayName)
		}

		labels := userProfileLabels(t, user.id)
		if labels["team"] != "a" || labels[identitySourceLabel] != "oidc" {
			t.Errorf("labels = %v, want team=a and %s=oidc kept", labels, identitySourceLabel)
		}
	})

	t.Run("external user cannot change their own email", func(t *testing.T) {
		user := createExternalEmailUser(t, "ldap", "ro-self")

		err := executeAsUser(t, GetTestDB(t), user.id, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(context.Background(),
				`UPDATE api.user_profiles SET spec = ROW('self@corp.test.local')::api.user_profile_spec WHERE id = $1`, user.id)
			return err
		})
		if err == nil || !strings.Contains(err.Error(), "10260") {
			t.Fatalf("self update = %v, want the 10260 error", err)
		}
	})

	t.Run("local user: admin can change the email and gets no label", func(t *testing.T) {
		user := createLocalEmailUser(t, "ro-local")
		newEmail := "new-" + user.email

		code, raw := patchProfile(t, admin, user.id, map[string]any{"spec": map[string]any{"email": newEmail}})
		if code != http.StatusOK {
			t.Fatalf("PATCH email = HTTP %d, want 200: %s", code, raw)
		}

		if got := userProfileEmail(t, user.id); got != newEmail {
			t.Errorf("spec.email = %q, want %q", got, newEmail)
		}

		if labels := userProfileLabels(t, user.id); labels[identitySourceLabel] != "" {
			t.Errorf("local user has label %s=%q", identitySourceLabel, labels[identitySourceLabel])
		}
	})

	t.Run("local user: the label cannot be forged", func(t *testing.T) {
		user := createLocalEmailUser(t, "ro-forge")

		metadata := profileMetadata(t, admin, user.id)
		metadata["labels"] = map[string]any{identitySourceLabel: "ldap"}

		code, raw := patchProfile(t, admin, user.id, map[string]any{"metadata": metadata})
		if code != http.StatusOK {
			t.Fatalf("metadata PATCH = HTTP %d, want 200: %s", code, raw)
		}

		if labels := userProfileLabels(t, user.id); labels[identitySourceLabel] != "" {
			t.Errorf("forged label kept: %v", labels)
		}
	})
}

// The label is set on both paths a profile gets its identity source: the
// GoTrue admin API (set by the UPDATE after the insert) and a row inserted
// with app_metadata already set.
func TestUserProfileExternalEmail_Label(t *testing.T) {
	user := createExternalEmailUser(t, "ldap", "lbl")
	if got := userProfileLabels(t, user.id)[identitySourceLabel]; got != "ldap" {
		t.Errorf("admin API user: label = %q, want ldap", got)
	}

	db := GetTestDB(t)
	ctx := context.Background()
	run := fmt.Sprintf("lbl-insert-%d", time.Now().UnixNano())

	var id string
	if err := db.QueryRowContext(ctx, `
		INSERT INTO auth.users (instance_id, id, aud, role, email, raw_app_meta_data, raw_user_meta_data, created_at, updated_at)
		VALUES ('00000000-0000-0000-0000-000000000000', gen_random_uuid(), 'authenticated', 'authenticated', $1,
		        '{"identity_source": "oidc"}', jsonb_build_object('preferred_username', $2::text, 'email', $3::text), now(), now())
		RETURNING id`,
		run+"@kc.oidc.neutree.local", run, run+"@corp.test.local").Scan(&id); err != nil {
		t.Fatalf("insert auth user: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM api.user_profiles WHERE id = $1`, id)
		_, _ = db.ExecContext(ctx, `DELETE FROM auth.users WHERE id = $1`, id)
	})

	if got := userProfileLabels(t, id)[identitySourceLabel]; got != "oidc" {
		t.Errorf("inserted user: label = %q, want oidc", got)
	}

	if got := userProfileEmail(t, id); got != run+"@corp.test.local" {
		t.Errorf("inserted user: spec.email = %q, want the directory mail", got)
	}
}

// The 107 backfill writes the directory mail, which the guard lets through.
func TestUserProfileExternalEmail_BackfillStillApplies(t *testing.T) {
	user := createExternalEmailUser(t, "ldap", "backfill")
	db := GetTestDB(t)
	ctx := context.Background()

	// Put the placeholder back as a pre-107 profile had it, bypassing triggers.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{
		`SET LOCAL session_replication_role = replica`,
		`UPDATE api.user_profiles p SET spec = ROW(u.email)::api.user_profile_spec FROM auth.users u WHERE p.id = u.id AND p.id = '` + user.id + `'`,
		`SET LOCAL session_replication_role = origin`,
		// The 107 backfill, verbatim.
		`UPDATE api.user_profiles p
		 SET spec = ROW(u.raw_user_meta_data->>'email')::api.user_profile_spec
		 FROM auth.users u
		 WHERE p.id = u.id
		   AND NULLIF(u.raw_app_meta_data->>'identity_source', '') IS NOT NULL
		   AND NULLIF(u.raw_user_meta_data->>'email', '') IS NOT NULL
		   AND (p.spec).email IS DISTINCT FROM u.raw_user_meta_data->>'email'`,
	} {
		if _, err := tx.ExecContext(ctx, stmt); err != nil {
			t.Fatalf("%s: %v", stmt, err)
		}
	}

	if err := tx.Commit(); err != nil {
		t.Fatalf("commit: %v", err)
	}

	if got := userProfileEmail(t, user.id); got != user.directory {
		t.Errorf("spec.email = %q, want the directory mail %q", got, user.directory)
	}
}

const identitySourceNameLabel = "neutree.ai/identity-source-name"

// The 108 backfill of app_metadata.identity_source_name, verbatim.
const identitySourceNameBackfill = `
UPDATE auth.users u
SET raw_app_meta_data = COALESCE(u.raw_app_meta_data, '{}'::jsonb)
                        || jsonb_build_object('identity_source_name', l.name)
FROM (
  SELECT DISTINCT ON (user_id) user_id, substr(source, strpos(source, ':') + 1) AS name
    FROM api.external_identities
   WHERE strpos(source, ':') > 0
   ORDER BY user_id, created_at
) l
WHERE u.id = l.user_id
  AND NULLIF(u.raw_app_meta_data->>'identity_source', '') IS NOT NULL
  AND NULLIF(u.raw_app_meta_data->>'identity_source_name', '') IS NULL`

func createNamedExternalUser(t *testing.T, sourceType, sourceName, prefix string) string {
	t.Helper()

	run := fmt.Sprintf("%s-%d", prefix, time.Now().UnixNano())

	return createSSOShapedUser(t, map[string]any{
		"email":         run + "@" + sourceName + "." + sourceType + ".neutree.local",
		"email_confirm": true,
		"app_metadata":  map[string]any{"identity_source": sourceType, "identity_source_name": sourceName},
		"user_metadata": map[string]any{"preferred_username": run, "email": run + "@corp.test.local"},
	})
}

func TestUserProfileExternalEmail_SourceNameLabel(t *testing.T) {
	admin := adminToken(t)

	t.Run("external user gets both labels", func(t *testing.T) {
		id := createNamedExternalUser(t, "ldap", "org-ldap", "name")

		labels := userProfileLabels(t, id)
		if labels[identitySourceLabel] != "ldap" || labels[identitySourceNameLabel] != "org-ldap" {
			t.Errorf("labels = %v, want %s=ldap and %s=org-ldap", labels, identitySourceLabel, identitySourceNameLabel)
		}
	})

	t.Run("admin cannot change or drop the label", func(t *testing.T) {
		id := createNamedExternalUser(t, "oidc", "keycloak", "name-admin")

		metadata := profileMetadata(t, admin, id)
		metadata["labels"] = map[string]any{identitySourceNameLabel: "forged", "team": "a"}

		code, raw := patchProfile(t, admin, id, map[string]any{"metadata": metadata})
		if code != http.StatusOK {
			t.Fatalf("metadata PATCH = HTTP %d, want 200: %s", code, raw)
		}

		labels := userProfileLabels(t, id)
		if labels[identitySourceNameLabel] != "keycloak" || labels["team"] != "a" {
			t.Errorf("labels = %v, want %s=keycloak and team=a", labels, identitySourceNameLabel)
		}

		metadata["labels"] = map[string]any{}

		if code, raw := patchProfile(t, admin, id, map[string]any{"metadata": metadata}); code != http.StatusOK {
			t.Fatalf("metadata PATCH = HTTP %d, want 200: %s", code, raw)
		}

		if got := userProfileLabels(t, id)[identitySourceNameLabel]; got != "keycloak" {
			t.Errorf("label after dropping it = %q, want keycloak", got)
		}
	})

	t.Run("user cannot change their own label", func(t *testing.T) {
		id := createNamedExternalUser(t, "ldap", "org-ldap", "name-self")

		var got string

		err := executeAsUser(t, GetTestDB(t), id, func(tx *sql.Tx) error {
			ctx := context.Background()
			if _, err := tx.ExecContext(ctx, `
				UPDATE api.user_profiles
				SET metadata.labels = jsonb_build_object($2::text, 'forged')::json
				WHERE id = $1`, id, identitySourceNameLabel); err != nil {
				return err
			}

			return tx.QueryRowContext(ctx,
				`SELECT COALESCE((metadata).labels::jsonb->>$2, '') FROM api.user_profiles WHERE id = $1`,
				id, identitySourceNameLabel).Scan(&got)
		})
		if err != nil {
			t.Fatalf("self update: %v", err)
		}

		if got != "org-ldap" {
			t.Errorf("label after self update = %q, want org-ldap", got)
		}
	})

	t.Run("user cannot set their own app_metadata", func(t *testing.T) {
		user := createLocalEmailUser(t, "name-gotrue")
		password := "Passw0rd-" + strings.TrimSuffix(user.email, "@corp.test.local")
		session := gotruePasswordLogin(t, user.email, password)

		code, raw := gotrueRequest(t, http.MethodPut, "/user", session.AccessToken,
			map[string]any{"app_metadata": map[string]any{"identity_source": "ldap", "identity_source_name": "forged"}})
		if code < 400 {
			t.Errorf("PUT /user with app_metadata = HTTP %d, want it refused: %s", code, raw)
		}

		if labels := userProfileLabels(t, user.id); labels[identitySourceLabel] != "" || labels[identitySourceNameLabel] != "" {
			t.Errorf("local user got labels %v", labels)
		}
	})

	t.Run("local user has no label and cannot forge it", func(t *testing.T) {
		user := createLocalEmailUser(t, "name-local")

		metadata := profileMetadata(t, admin, user.id)
		metadata["labels"] = map[string]any{identitySourceNameLabel: "org-ldap"}

		if code, raw := patchProfile(t, admin, user.id, map[string]any{"metadata": metadata}); code != http.StatusOK {
			t.Fatalf("metadata PATCH = HTTP %d, want 200: %s", code, raw)
		}

		if got := userProfileLabels(t, user.id)[identitySourceNameLabel]; got != "" {
			t.Errorf("local user has label %s=%q", identitySourceNameLabel, got)
		}
	})

	t.Run("backfill takes the name from the link", func(t *testing.T) {
		db := GetTestDB(t)
		ctx := context.Background()
		user := createExternalEmailUser(t, "ldap", "name-backfill")
		local := createLocalEmailUser(t, "name-backfill-local")

		if _, err := db.ExecContext(ctx,
			`INSERT INTO api.external_identities (source, external_id, user_id) VALUES ('ldap:org-ldap', $1, $2)`,
			"ext-"+user.id, user.id); err != nil {
			t.Fatalf("insert link: %v", err)
		}

		if got := userProfileLabels(t, user.id)[identitySourceNameLabel]; got != "" {
			t.Fatalf("label before backfill = %q, want none", got)
		}

		if _, err := db.ExecContext(ctx, identitySourceNameBackfill); err != nil {
			t.Fatalf("backfill: %v", err)
		}

		var name string
		if err := db.QueryRowContext(ctx,
			`SELECT raw_app_meta_data->>'identity_source_name' FROM auth.users WHERE id = $1`, user.id).Scan(&name); err != nil || name != "org-ldap" {
			t.Errorf("app_metadata.identity_source_name = %q (%v), want org-ldap", name, err)
		}

		labels := userProfileLabels(t, user.id)
		if labels[identitySourceLabel] != "ldap" || labels[identitySourceNameLabel] != "org-ldap" {
			t.Errorf("labels after backfill = %v, want %s=ldap and %s=org-ldap", labels, identitySourceLabel, identitySourceNameLabel)
		}

		if got := userProfileLabels(t, local.id)[identitySourceNameLabel]; got != "" {
			t.Errorf("local user has label %s=%q after backfill", identitySourceNameLabel, got)
		}
	})
}
