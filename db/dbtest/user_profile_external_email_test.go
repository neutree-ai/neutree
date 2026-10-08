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

// These tests cover migration 106: the profile email of a user from an
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

// The 103 backfill writes the directory mail, which the guard lets through.
func TestUserProfileExternalEmail_BackfillStillApplies(t *testing.T) {
	user := createExternalEmailUser(t, "ldap", "backfill")
	db := GetTestDB(t)
	ctx := context.Background()

	// Put the placeholder back as a pre-103 profile had it, bypassing triggers.
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("begin: %v", err)
	}
	defer func() { _ = tx.Rollback() }()

	for _, stmt := range []string{
		`SET LOCAL session_replication_role = replica`,
		`UPDATE api.user_profiles p SET spec = ROW(u.email)::api.user_profile_spec FROM auth.users u WHERE p.id = u.id AND p.id = '` + user.id + `'`,
		`SET LOCAL session_replication_role = origin`,
		// The 103 backfill, verbatim.
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
