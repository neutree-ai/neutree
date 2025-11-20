package dbtest

import (
	"context"
	"database/sql"
	"testing"
)

func TestPresetRoleProtection_CannotUpdatePresetRole(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "role-updater", "roleupdater@example.com",
		[]string{"role:read", "role:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.roles
			SET spec = ROW((spec).preset_key, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
			WHERE (metadata).name = 'admin'
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
	t.Logf("preset role update blocked by RLS (0 rows affected)")
}

func TestPresetRoleProtection_CannotDeletePresetRole(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "role-deleter", "roledeleter@example.com",
		[]string{"role:read", "role:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.roles
			WHERE (metadata).name = 'admin'
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
	t.Logf("preset role delete blocked by RLS (0 rows affected)")
}

func TestPresetRoleProtection_CanUpdateCustomRole(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'Role',
			ROW('custom-role', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
		)
	`)
	if err != nil {
		t.Fatalf("failed to create custom role: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "role-updater2", "roleupdater2@example.com",
		[]string{"role:read", "role:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.roles
			SET spec = ROW((spec).preset_key, ARRAY['workspace:read', 'workspace:create']::api.permission_action[])::api.role_spec
			WHERE (metadata).name = 'custom-role'
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
		t.Fatalf("custom role update should succeed: %v", err)
	}
	t.Logf("custom role update succeeded")
}

func TestPresetRoleProtection_CanDeleteCustomRole(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	_, err = tx1.ExecContext(ctx, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'Role',
			ROW('deletable-role', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, ARRAY['workspace:read']::api.permission_action[])::api.role_spec
		)
	`)
	if err != nil {
		t.Fatalf("failed to create custom role: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "role-deleter2", "roledeleter2@example.com",
		[]string{"role:read", "role:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.roles
			WHERE (metadata).name = 'deletable-role'
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
		t.Fatalf("custom role delete should succeed: %v", err)
	}
	t.Logf("custom role delete succeeded")
}
