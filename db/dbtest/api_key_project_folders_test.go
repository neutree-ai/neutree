package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

func grantWorkspaceRead(t *testing.T, db *sql.DB, userID, suffix string) {
	t.Helper()
	roleName := "api-key-project-" + suffix
	if _, err := db.Exec(`
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1', 'Role',
			ROW($1, NULL, NULL, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
		);
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1', 'RoleAssignment',
			ROW($2, NULL, NULL, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($3::uuid, NULL, TRUE, $1)::api.role_assignment_spec
		)
	`, roleName, roleName+"-assignment", userID); err != nil {
		t.Fatalf("grant workspace read: %v", err)
	}
}

func TestAPIKeyProjectFolders(t *testing.T) {
	db := GetTestDB(t)
	alice := CreateTestUser(t, "folder-alice", "folder-alice@example.com", "password")
	bob := CreateTestUser(t, "folder-bob", "folder-bob@example.com", "password")
	grantWorkspaceRead(t, db, alice.ID, "alice")
	grantWorkspaceRead(t, db, bob.ID, "bob")

	var projectID string
	WithUserContext(t, db, alice.ID, func(tx *sql.Tx) {
		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key_project('default', '客服系统', '共享文件夹')
		`).Scan(&projectID); err != nil {
			t.Fatalf("create shared project: %v", err)
		}

		var keyID string
		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key(
				'default', 'apikey-alice', 0, '客服系统生产 Key',
				NULL, NULL, NULL, 'Alice key'
			)
		`).Scan(&keyID); err != nil {
			t.Fatalf("create ungrouped key: %v", err)
		}

		var project sql.NullString
		if err := tx.QueryRow(
			`SELECT project_id FROM api.api_keys WHERE id=$1`, keyID,
		).Scan(&project); err != nil || project.Valid {
			t.Fatalf("new key project=%v err=%v, want NULL", project, err)
		}

		if _, err := tx.Exec(
			`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid], $2)`,
			keyID, projectID,
		); err != nil {
			t.Fatalf("move key into project: %v", err)
		}
		if _, err := tx.Exec(
			`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid], NULL)`, keyID,
		); err != nil {
			t.Fatalf("move key to ungrouped: %v", err)
		}
	})

	WithUserContext(t, db, bob.ID, func(tx *sql.Tx) {
		var visibleID string
		if err := tx.QueryRow(`
			SELECT id FROM api.list_api_key_projects('default')
			WHERE name = '客服系统'
		`).Scan(&visibleID); err != nil || visibleID != projectID {
			t.Fatalf("shared project id=%q err=%v, want %q", visibleID, err, projectID)
		}

		if _, err := tx.Exec(`
			SELECT api.create_api_key_project('default', '客服系统', '')
		`); err == nil || !strings.Contains(err.Error(), "already exists") {
			t.Fatalf("expected workspace-wide duplicate rejection, got %v", err)
		}

		if _, err := tx.Exec(`
			SELECT api.create_api_key(
				'default', 'apikey-bob', 0, '客服系统生产 Key',
				NULL, NULL, $1, 'Bob key'
			)
		`, projectID); err != nil {
			t.Fatalf("duplicate display name in shared project: %v", err)
		}
	})
}
