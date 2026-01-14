package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

func TestPresetRoleAssignmentProtection_CannotUpdateAdminGlobalAssignment(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-updater", "raupdater@example.com",
		[]string{"role_assignment:read", "role_assignment:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.role_assignments
			SET spec = ROW((spec).user_id, (spec).workspace, (spec).global, 'workspace-user')::api.role_assignment_spec
			WHERE (metadata).name = 'admin-global-role-assignment'
		`)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS error: admin global role assignment should not be updatable")
	}
	t.Logf("admin global role assignment update blocked by RLS: %v", err)
}

func TestPresetRoleAssignmentProtection_CannotDeleteAdminGlobalAssignment(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-deleter", "radeleter@example.com",
		[]string{"role_assignment:read", "role_assignment:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.role_assignments
			WHERE (metadata).name = 'admin-global-role-assignment'
		`)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 0 {
			t.Fatalf("expected 0 rows affected by RLS policy, got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("admin global role assignment delete blocked by RLS (0 rows affected)")
}

func TestPresetRoleAssignmentProtection_CanUpdateOtherGlobalAssignment(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	testUser := CreateTestUser(t, "test-global-user", "testglobaluser@example.com", "password123")

	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW('test-global-assignment', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, NULL, TRUE, 'workspace-user')::api.role_assignment_spec
		)
	`, testUser.ID)
	if err != nil {
		t.Fatalf("failed to create global role assignment: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-updater2", "raupdater2@example.com",
		[]string{"role_assignment:read", "role_assignment:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.role_assignments
			SET metadata = ROW(
				(metadata).name,
				'Updated Display Name',
				(metadata).workspace,
				(metadata).deletion_timestamp,
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE (metadata).name = 'test-global-assignment'
		`)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 1 {
			t.Fatalf("expected 1 row affected, got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("global role assignment update should succeed: %v", err)
	}
	t.Logf("global role assignment update succeeded")
}

func TestPresetRoleAssignmentProtection_CanDeleteOtherGlobalAssignment(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	testUser := CreateTestUser(t, "test-deletable-user", "testdeletableuser@example.com", "password123")

	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW('test-deletable-assignment', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, NULL, TRUE, 'workspace-user')::api.role_assignment_spec
		)
	`, testUser.ID)
	if err != nil {
		t.Fatalf("failed to create global role assignment: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-deleter2", "radeleter2@example.com",
		[]string{"role_assignment:read", "role_assignment:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.role_assignments
			WHERE (metadata).name = 'test-deletable-assignment'
		`)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 1 {
			t.Fatalf("expected 1 row affected, got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("global role assignment delete should succeed: %v", err)
	}
	t.Logf("global role assignment delete succeeded")
}
