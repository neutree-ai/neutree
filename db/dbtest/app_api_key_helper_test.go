package dbtest

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/google/uuid"
)

// Helpers for the model:invoke, Team API key and team-admin tests. Users are
// written straight into auth.users (no GoTrue call); checks run through
// asUser, which rolls back.

// rawUser stores a user and returns its id; it is removed when the test ends.
func rawUser(t *testing.T, db *sql.DB, prefix string) string {
	t.Helper()

	id := uuid.NewString()
	name := newOrgName(prefix)
	mustExec(t, db, `
		INSERT INTO auth.users (id, instance_id, aud, role, email, encrypted_password,
		                        raw_user_meta_data, created_at, updated_at)
		VALUES ($1, '00000000-0000-0000-0000-000000000000', 'authenticated', 'authenticated',
		        $2, 'x', jsonb_build_object('username', $3::text), now(), now())`,
		id, name+"@appkey.test.local", name)

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).user_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM api.api_keys WHERE user_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM api.group_daily_usage WHERE user_id = $1`, id)
		_, _ = db.Exec(`DELETE FROM auth.users WHERE id = $1`, id)
	})

	return id
}

// customRole stores a role holding exactly perms and returns its name.
func customRole(t *testing.T, db *sql.DB, perms ...string) string {
	t.Helper()

	name := newOrgName("role")
	mustExec(t, db, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES ('v1', 'Role', ROW($1, NULL, NULL, NULL, now(), now(), NULL, NULL)::api.metadata,
		        ROW(NULL, $2::text[]::api.permission_action[])::api.role_spec)`,
		name, "{"+strings.Join(perms, ",")+"}")

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.role_assignments WHERE (spec).role = $1`, name)
		_, _ = db.Exec(`DELETE FROM api.roles WHERE (metadata).name = $1`, name)
	})

	return name
}

// bindGlobal assigns role to the user globally, as postgres.
func bindGlobal(t *testing.T, db *sql.DB, userID, role string) {
	t.Helper()

	seedRoleAssignment(t, db, roleAssignment{UserID: userID, Global: true, Role: role, SubjectKind: "User"})
}

// createTestAPIKey creates an API key of userID in workspace through the RPC;
// quota 0 is no quota of its own.
func createTestAPIKey(t *testing.T, userID, workspace string, quota int) string {
	t.Helper()

	db := GetTestDB(t)

	var keyID string
	if err := execWithContext(t, db, []SetContextFunc{setUserContext(userID), setJwtSecretContext()}, func(tx *sql.Tx) error {
		return tx.QueryRow(`SELECT id FROM api.create_api_key(p_workspace := $1, p_name := $2, p_quota := $3)`,
			workspace, newOrgName("key"), quota).Scan(&keyID)
	}); err != nil {
		t.Fatalf("creating an API key failed: %v", err)
	}

	t.Cleanup(func() {
		_, _ = db.Exec(`DELETE FROM api.api_daily_usage WHERE (spec).api_key_id = $1`, keyID)
		_, _ = db.Exec(`DELETE FROM api.api_keys WHERE id = $1`, keyID)
	})

	return keyID
}

// readMigration returns the SQL of a migration file of this repository.
func readMigration(t *testing.T, name string) string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Join("..", "migrations", name))
	if err != nil {
		t.Fatalf("reading migration %s: %v", name, err)
	}

	return string(raw)
}

func rolePermissions(t *testing.T, tx *sql.Tx, role string) string {
	t.Helper()

	var perms string
	if err := tx.QueryRow(`SELECT (spec).permissions::text FROM api.roles WHERE (metadata).name = $1`, role).Scan(&perms); err != nil {
		t.Fatalf("reading role %s: %v", role, err)
	}

	return perms
}

func servedEndpointStatus(model string) string {
	return fmt.Sprintf(`{"phase":"Running","served_models":[%q]}`, model)
}
