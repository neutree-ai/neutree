package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

func createCustomRole(t *testing.T, tx *sql.Tx, name string) int {
	t.Helper()
	var roleID int
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'Role',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, ARRAY['endpoint:read']::api.permission_action[])::api.role_spec
		)
		RETURNING id
	`, name).Scan(&roleID)
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}
	return roleID
}

func TestRoleSoftDelete_WithoutDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	roleID := createCustomRole(t, tx1, "role-soft-delete-test-1")
	userID := createUserWithPermissions(t, tx1, "role-updater-1", "roleupdater1@example.com",
		[]string{"role:read", "role:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.roles
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE id = $1
		`, roleID)
		return err
	})

	if err == nil {
		t.Fatal("expected error: user with only update permission should not be able to soft delete role")
	}
	t.Logf("role soft delete blocked: %v", err)
}

func TestRoleSoftDelete_WithDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	roleID := createCustomRole(t, tx1, "role-soft-delete-test-2")
	userID := createUserWithPermissions(t, tx1, "role-deleter-1", "roledeleter1@example.com",
		[]string{"role:read", "role:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.roles
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE id = $1
		`, roleID)
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
		t.Fatalf("role soft delete should succeed: %v", err)
	}
}

func TestRoleSoftDelete_PresetRoleCannotBeSoftDeleted(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "role-sd-deleter-3", "rolesdd3@example.com",
		[]string{"role:read", "role:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.roles
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE (metadata).name = 'admin'
		`)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS error: preset role should not be soft deletable")
	}
	t.Logf("preset role soft delete blocked by RLS: %v", err)
}

func TestRoleAssignmentSoftDelete_WithoutDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	testUser := CreateTestUser(t, "ra-target-1", "ratarget1@example.com", "password123")
	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW('ra-soft-delete-test-1', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, NULL, TRUE, 'workspace-user')::api.role_assignment_spec
		)
	`, testUser.ID)
	if err != nil {
		t.Fatalf("failed to create role assignment: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-updater-sd-1", "raupdatersd1@example.com",
		[]string{"role_assignment:read", "role_assignment:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.role_assignments
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE (metadata).name = 'ra-soft-delete-test-1'
		`)
		return err
	})

	if err == nil {
		t.Fatal("expected error: user with only update permission should not be able to soft delete role assignment")
	}
	t.Logf("role assignment soft delete blocked: %v", err)
}

func TestRoleAssignmentSoftDelete_WithDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	testUser := CreateTestUser(t, "ra-target-2", "ratarget2@example.com", "password123")
	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW('ra-soft-delete-test-2', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($1::uuid, NULL, TRUE, 'workspace-user')::api.role_assignment_spec
		)
	`, testUser.ID)
	if err != nil {
		t.Fatalf("failed to create role assignment: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-deleter-sd-1", "radeletersd1@example.com",
		[]string{"role_assignment:read", "role_assignment:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.role_assignments
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE (metadata).name = 'ra-soft-delete-test-2'
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
		t.Fatalf("role assignment soft delete should succeed: %v", err)
	}
}

func TestRoleAssignmentSoftDelete_AdminAssignmentCannotBeSoftDeleted(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "ra-sd-deleter-3", "rasdd3@example.com",
		[]string{"role_assignment:read", "role_assignment:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.role_assignments
			SET metadata = ROW(
				(metadata).name,
				(metadata).display_name,
				(metadata).workspace,
				now(),
				(metadata).creation_timestamp,
				(metadata).update_timestamp,
				(metadata).labels,
				(metadata).annotations
			)::api.metadata
			WHERE (metadata).name = 'admin-global-role-assignment'
		`)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS error: admin role assignment should not be soft deletable")
	}
	t.Logf("admin role assignment soft delete blocked by RLS: %v", err)
}
