package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

func TestGatewayACLPermissionsResolveAndRevoke(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	var userID string
	var roleID int
	var assignmentID int

	err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		userID = createUserWithPermissions(t, tx, "gateway-acl-user", "gateway-acl-user@example.com", []string{
			"endpoint:read",
			"external_endpoint:read",
		})

		if err := tx.QueryRowContext(ctx, `
			SELECT id FROM api.roles
			WHERE (metadata).name = 'gateway-acl-user-role'
		`).Scan(&roleID); err != nil {
			return err
		}

		return tx.QueryRowContext(ctx, `
			SELECT id FROM api.role_assignments
			WHERE (spec).user_id = $1::uuid
			AND (spec).role = 'gateway-acl-user-role'
		`, userID).Scan(&assignmentID)
	})
	if err != nil {
		t.Fatalf("failed to seed ACL permission user: %v", err)
	}

	assertHasPermission := func(permission string, expected bool) {
		t.Helper()

		var allowed bool
		err := execWithContext(t, db, []SetContextFunc{setUserContext(userID)}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT api.has_permission($1::uuid, $2::api.permission_action, 'workspace-a')
			`, userID, permission).Scan(&allowed)
		})
		if err != nil {
			t.Fatalf("failed to call has_permission(%s): %v", permission, err)
		}
		if allowed != expected {
			t.Fatalf("has_permission(%s) = %v, want %v", permission, allowed, expected)
		}
	}

	assertHasPermission("endpoint:read", true)
	assertHasPermission("external_endpoint:read", true)

	_, err = db.ExecContext(ctx, `DELETE FROM api.role_assignments WHERE id = $1`, assignmentID)
	if err != nil {
		t.Fatalf("failed to remove role assignment: %v", err)
	}

	assertHasPermission("endpoint:read", false)
	assertHasPermission("external_endpoint:read", false)

	_, _ = db.ExecContext(ctx, `DELETE FROM api.roles WHERE id = $1`, roleID)
}
