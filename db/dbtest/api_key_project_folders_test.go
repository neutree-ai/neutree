package dbtest

import (
	"database/sql"
	"strings"
	"testing"
)

func grantWorkspaceRead(t *testing.T, db *sql.DB, userID, suffix string) {
	t.Helper()
	roleName := "api-key-project-" + suffix

	// One statement per Exec: a parameterised Exec is sent as a prepared
	// statement, and PostgreSQL rejects multiple commands in one of those.
	if _, err := db.Exec(`
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1', 'Role',
			ROW($1, NULL, NULL, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
		)
	`, roleName); err != nil {
		t.Fatalf("grant workspace read (role): %v", err)
	}

	if _, err := db.Exec(`
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1', 'RoleAssignment',
			ROW($1, NULL, NULL, NULL, now(), now(), '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, NULL, TRUE, $3)::api.role_assignment_spec
		)
	`, roleName+"-assignment", userID, roleName); err != nil {
		t.Fatalf("grant workspace read (assignment): %v", err)
	}
}

// api.create_api_key signs the key value, so any block that creates a key needs
// the JWT secret in scope as well as the user context. WithUserContext sets only
// the latter.
func withKeyOwnerContext(t *testing.T, db *sql.DB, userID string, fn func(*sql.Tx)) {
	t.Helper()
	err := execWithContext(
		t, db,
		[]SetContextFunc{setUserContext(userID), setJwtSecretContext()},
		func(tx *sql.Tx) error { fn(tx); return nil },
	)
	if err != nil {
		t.Fatalf("key owner context block: %v", err)
	}
}

func TestAPIKeyProjectFolders(t *testing.T) {
	db := GetTestDB(t)
	alice := CreateTestUser(t, "folder-alice", "folder-alice@example.com", "password")
	bob := CreateTestUser(t, "folder-bob", "folder-bob@example.com", "password")
	grantWorkspaceRead(t, db, alice.ID, "alice")
	grantWorkspaceRead(t, db, bob.ID, "bob")

	var projectID string
	var keyID string
	withKeyOwnerContext(t, db, alice.ID, func(tx *sql.Tx) {
		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key_project('default', '客服系统', '共享文件夹')
		`).Scan(&projectID); err != nil {
			t.Fatalf("create shared project: %v", err)
		}

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
			`SELECT (spec).project_id FROM api.api_keys WHERE id=$1`, keyID,
		).Scan(&project); err != nil || project.Valid {
			t.Fatalf("new key project=%v err=%v, want NULL", project, err)
		}

		if _, err := tx.Exec(
			`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid], $2)`,
			keyID, projectID,
		); err != nil {
			t.Fatalf("move key into project: %v", err)
		}

		var secretFields int
		if err := tx.QueryRow(`
			SELECT count(*)
			FROM api.get_api_key_project_groups('default', NULL, NULL, 1, 20) g,
			     jsonb_array_elements(g.api_keys) key
			WHERE key->>'id' = $1
			  AND key #> '{status,sk_value}' IS NOT NULL
		`, keyID).Scan(&secretFields); err != nil {
			t.Fatalf("inspect grouped API key: %v", err)
		}
		if secretFields != 0 {
			t.Fatalf("grouped API key exposed %d secret fields, want 0", secretFields)
		}
		if _, err := tx.Exec(
			`SELECT api.move_api_keys_to_project(ARRAY[$1::uuid], NULL)`, keyID,
		); err != nil {
			t.Fatalf("move key to ungrouped: %v", err)
		}

		if _, err := tx.Exec(`
			SELECT api.update_api_key_configuration(
				$1, $2, '{}'::jsonb, '客服系统正式 Key', 'Production calls'
			)
		`, keyID, projectID); err != nil {
			t.Fatalf("update API key details: %v", err)
		}

		var technicalName, displayName, description string
		var updatedProject sql.NullString
		if err := tx.QueryRow(`
			SELECT (metadata).name, (metadata).display_name,
			       (spec).description, (spec).project_id
			FROM api.api_keys WHERE id = $1
		`, keyID).Scan(
			&technicalName, &displayName, &description, &updatedProject,
		); err != nil {
			t.Fatalf("read updated API key: %v", err)
		}
		if technicalName != "apikey-alice" {
			t.Fatalf("technical name=%q, want unchanged", technicalName)
		}
		if displayName != "客服系统正式 Key" || description != "Production calls" {
			t.Fatalf("updated identity=(%q, %q), want new values", displayName, description)
		}
		if !updatedProject.Valid || updatedProject.String != projectID {
			t.Fatalf("updated project=%v, want %s", updatedProject, projectID)
		}

	})

	err := execWithContext(t, db, []SetContextFunc{setUserContext(alice.ID)}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			SELECT api.update_api_key_configuration(
				$1, NULL, '{}'::jsonb, '   ', ''
			)
		`, keyID)
		return err
	})
	if err == nil || !strings.Contains(err.Error(), "display name is required") {
		t.Fatalf("expected blank display name rejection, got %v", err)
	}

	var bobProjectID string
	withKeyOwnerContext(t, db, bob.ID, func(tx *sql.Tx) {
		var visibleCount int
		if err := tx.QueryRow(`
			SELECT count(*) FROM api.list_api_key_projects('default')
			WHERE id = $1
		`, projectID).Scan(&visibleCount); err != nil || visibleCount != 0 {
			t.Fatalf("other user's project count=%d err=%v, want 0", visibleCount, err)
		}

		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key_project('default', '客服系统', '')
		`).Scan(&bobProjectID); err != nil {
			t.Fatalf("same project name for another user: %v", err)
		}

		var technicalName string
		if err := tx.QueryRow(`
			SELECT (metadata).name FROM api.create_api_key(
				'default', NULL, 0, '客服系统生产 Key',
				NULL, NULL, $1, 'Bob key'
			)
		`, bobProjectID).Scan(&technicalName); err != nil {
			t.Fatalf("backend-generated technical name: %v", err)
		}
		if !strings.HasPrefix(technicalName, "apikey-") {
			t.Fatalf("generated technical name=%q, want apikey- prefix", technicalName)
		}
	})

	for name, query := range map[string]string{
		"update another user's project": `SELECT api.update_api_key_project($1, 'renamed', NULL)`,
		"delete another user's project": `SELECT api.delete_api_key_project($1)`,
	} {
		t.Run(name, func(t *testing.T) {
			err := execWithContext(t, db, []SetContextFunc{setUserContext(bob.ID)}, func(tx *sql.Tx) error {
				_, err := tx.Exec(query, projectID)
				return err
			})
			if err == nil {
				t.Fatalf("expected %s to fail", name)
			}
		})
	}

	err = execWithContext(t, db, []SetContextFunc{setUserContext(bob.ID)}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`
			SELECT api.create_api_key(
				'default', 'apikey-bob-denied', 0, '客服系统生产 Key',
				NULL, NULL, $1, 'Bob key'
			)
		`, projectID)
		return err
	})
	if err == nil {
		t.Fatal("expected assigning an API key to another user's project to fail")
	}

	withKeyOwnerContext(t, db, alice.ID, func(tx *sql.Tx) {
		if _, err := tx.Exec(`
			SELECT api.create_api_key_project('default', 'Support', '')
		`); err != nil {
			t.Fatalf("create project for case-insensitive uniqueness test: %v", err)
		}
	})

	for name, projectName := range map[string]string{
		"exact duplicate":            "客服系统",
		"case-insensitive duplicate": "support",
	} {
		t.Run(name, func(t *testing.T) {
			err := execWithContext(t, db, []SetContextFunc{setUserContext(alice.ID)}, func(tx *sql.Tx) error {
				_, err := tx.Exec(`SELECT api.create_api_key_project('default', $1, '')`, projectName)
				return err
			})
			if err == nil || !strings.Contains(err.Error(), "already exists") {
				t.Fatalf("expected %s rejection, got %v", name, err)
			}
		})
	}
}

// A Project whose keys have all been soft-deleted reads as empty everywhere in
// the UI, so deleting it must succeed. Counting soft-deleted keys in the guard
// made such a Project permanently undeletable.
func TestDeleteAPIKeyProjectIgnoresSoftDeletedKeys(t *testing.T) {
	db := GetTestDB(t)
	user := CreateTestUser(t, "folder-gc", "folder-gc@example.com", "password")
	grantWorkspaceRead(t, db, user.ID, "gc")

	var projectID, keyID string
	withKeyOwnerContext(t, db, user.ID, func(tx *sql.Tx) {
		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key_project('default', 'Retired', '')
		`).Scan(&projectID); err != nil {
			t.Fatalf("create project: %v", err)
		}
		if err := tx.QueryRow(`
			SELECT id FROM api.create_api_key(
				'default', NULL, 0, 'Retired key', NULL, NULL, $1, ''
			)
		`, projectID).Scan(&keyID); err != nil {
			t.Fatalf("create key in project: %v", err)
		}
	})

	// A live key still blocks deletion. This runs in its own transaction: the
	// raised exception aborts whichever transaction it lands in.
	err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID)}, func(tx *sql.Tx) error {
		_, err := tx.Exec(`SELECT api.delete_api_key_project($1)`, projectID)
		return err
	})
	if err == nil {
		t.Fatal("expected a Project holding a live API key to refuse deletion")
	}

	withKeyOwnerContext(t, db, user.ID, func(tx *sql.Tx) {
		if _, err := tx.Exec(`
			UPDATE api.api_keys
			SET metadata = ROW(
				(metadata).name, (metadata).display_name, (metadata).workspace,
				now(), (metadata).creation_timestamp, now(),
				(metadata).labels, (metadata).annotations
			)::api.metadata
			WHERE id = $1
		`, keyID); err != nil {
			t.Fatalf("soft delete key: %v", err)
		}

		var grouped int
		if err := tx.QueryRow(`
			SELECT api_key_count
			FROM api.get_api_key_project_groups('default', NULL, NULL, 1, 50)
			WHERE project->>'id' = $1
		`, projectID).Scan(&grouped); err != nil {
			t.Fatalf("read grouped count: %v", err)
		}
		if grouped != 0 {
			t.Fatalf("grouped count=%d after soft delete, want 0", grouped)
		}

		if _, err := tx.Exec(`SELECT api.delete_api_key_project($1)`, projectID); err != nil {
			t.Fatalf("delete Project whose only key is soft-deleted: %v", err)
		}
	})
}

// A search term is a literal, not a LIKE pattern: '%' must match the keys whose
// text actually contains a percent sign, not every key.
func TestAPIKeyProjectSearchEscapesWildcards(t *testing.T) {
	db := GetTestDB(t)
	user := CreateTestUser(t, "folder-like", "folder-like@example.com", "password")
	grantWorkspaceRead(t, db, user.ID, "like")

	withKeyOwnerContext(t, db, user.ID, func(tx *sql.Tx) {
		for _, name := range []string{"Plain project", "100% coverage"} {
			if _, err := tx.Exec(
				`SELECT api.create_api_key_project('default', $1, '')`, name,
			); err != nil {
				t.Fatalf("create project %q: %v", name, err)
			}
		}

		var matched int
		if err := tx.QueryRow(`
			SELECT count(*) FROM api.get_api_key_project_groups('default', '%', NULL, 1, 50)
		`).Scan(&matched); err != nil {
			t.Fatalf("search for a literal percent: %v", err)
		}
		if matched != 1 {
			t.Fatalf("searching %q matched %d groups, want only the one containing it", "%", matched)
		}

		var counted int
		if err := tx.QueryRow(`
			SELECT api.count_api_key_project_group_api_keys('default', '_', NULL)
		`).Scan(&counted); err != nil {
			t.Fatalf("count with a literal underscore: %v", err)
		}
		if counted != 0 {
			t.Fatalf("searching %q counted %d API keys, want 0", "_", counted)
		}
	})
}
