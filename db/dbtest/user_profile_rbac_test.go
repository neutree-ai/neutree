package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func createUserWithoutPermissions(t *testing.T, username, email string) string {
	t.Helper()
	user := CreateTestUser(t, username, email, "password123")
	return user.ID
}

func TestUserProfileRBAC_CanReadOwnProfile(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	userID := createUserWithoutPermissions(t, "regular-user-1", "user1@test.local")

	err := executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		var count int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.user_profiles WHERE id = $1
		`, userID).Scan(&count)
		if err != nil {
			return err
		}

		if count != 1 {
			t.Fatalf("expected to read own profile, got %d rows", count)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user can read own profile")
}

func TestUserProfileRBAC_CannotReadOthersProfileWithoutPermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	user1ID := createUserWithoutPermissions(t, "user1-noread", "user1noread@test.local")
	user2ID := createUserWithoutPermissions(t, "user2-noread", "user2noread@test.local")

	err := executeAsUser(t, adminDB, user1ID, func(tx *sql.Tx) error {
		var count int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.user_profiles WHERE id = $1
		`, user2ID).Scan(&count)
		if err != nil {
			return err
		}

		if count != 0 {
			t.Fatalf("expected to not read other user profile, got %d rows", count)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot read other user profile without permission")
}

func TestUserProfileRBAC_AdminCanReadAllProfiles(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	adminUserID := createUserWithPermissions(t, tx1, "admin-user-read", "adminread@test.local",
		[]string{"user_profile:read"})
	createUserWithoutPermissions(t, "test-user", "testuser@test.local")

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, adminUserID, func(tx *sql.Tx) error {
		var count int
		err := tx.QueryRowContext(ctx, `
			SELECT COUNT(*) FROM api.user_profiles
		`).Scan(&count)
		if err != nil {
			return err
		}

		if count < 2 {
			t.Fatalf("expected admin to read at least 2 profiles, got %d rows", count)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("admin can read all profiles")
}

func TestUserProfileRBAC_CanUpdateOwnProfile(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	userID := createUserWithoutPermissions(t, "user-canupdate", "usercanupdate@test.local")

	err := executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET spec = ROW('newemail@example.com')::api.user_profile_spec
			WHERE id = $1
		`, userID)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 1 {
			t.Fatalf("expected 1 row updated, got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user can update own profile")
}

func TestUserProfileRBAC_CannotUpdateOthersProfileWithoutPermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	user1ID := createUserWithoutPermissions(t, "user1-noupdate", "user1noupdate@test.local")
	user2ID := createUserWithoutPermissions(t, "user2-noupdate", "user2noupdate@test.local")

	err := executeAsUser(t, adminDB, user1ID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET spec = ROW('hacked@example.com')::api.user_profile_spec
			WHERE id = $1
		`, user2ID)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 0 {
			t.Fatalf("expected 0 rows updated (blocked by RLS), got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot update other user profile without permission")
}

func TestUserProfileRBAC_CannotModifyOwnUsername(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	userID := createUserWithoutPermissions(t, "user-username", "userusername@test.local")

	err := executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW('hacker', NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE id = $1
		`, userID)

		if err == nil {
			t.Fatalf("expected error when modifying username, got nil")
		}

		if !strings.Contains(err.Error(), "Cannot modify username") {
			t.Fatalf("expected 'Cannot modify username' error, got: %v", err)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot modify own username (trigger blocked)")
}

func TestUserProfileRBAC_CannotModifyOwnWorkspace(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	userID := createUserWithoutPermissions(t, "user-workspace", "userworkspace@test.local")

	err := executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW((metadata).name, NULL, 'hacked-workspace', NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
			WHERE id = $1
		`, userID)

		if err == nil {
			t.Fatalf("expected error when modifying workspace, got nil")
		}

		if !strings.Contains(err.Error(), "Cannot modify workspace") {
			t.Fatalf("expected 'Cannot modify workspace' error, got: %v", err)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot modify own workspace (trigger blocked)")
}

func TestUserProfileRBAC_CannotSoftDeleteSelf(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "user-softdel", "usersoftdel@test.local",
		[]string{"user_profile:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW((metadata).name, (metadata).display_name, (metadata).workspace, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, (metadata).labels, (metadata).annotations)::api.metadata
			WHERE id = $1
		`, userID)

		if err == nil {
			t.Fatalf("expected error when soft deleting self, got nil")
		}

		if !strings.Contains(err.Error(), "Cannot delete your own user profile") {
			t.Fatalf("expected 'Cannot delete your own user profile' error, got: %v", err)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot soft delete self (trigger blocked)")
}

func TestUserProfileRBAC_CannotHardDeleteSelf(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx1, "user-harddel", "userharddel@test.local",
		[]string{"user_profile:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.user_profiles WHERE id = $1
		`, userID)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 0 {
			t.Fatalf("expected 0 rows deleted (blocked by RLS), got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("user cannot hard delete self (RLS blocked)")
}

func TestUserProfileRBAC_AdminCanDeleteOtherUsers(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	adminUserID := createUserWithPermissions(t, tx1, "admin-deleter", "admindeleter@test.local",
		[]string{"user_profile:delete", "user_profile:update", "user_profile:read"})
	targetUserID := createUserWithoutPermissions(t, "target-user-del", "targetuserdel@test.local")

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	tx2, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	var name, displayName, workspace sql.NullString
	var labels, annotations string
	var creationTS, updateTS string

	err = tx2.QueryRowContext(ctx, `
		SELECT (metadata).name, (metadata).display_name, (metadata).workspace,
		       (metadata).creation_timestamp, (metadata).update_timestamp,
		       (metadata).labels, (metadata).annotations
		FROM api.user_profiles WHERE id = $1
	`, targetUserID).Scan(&name, &displayName, &workspace, &creationTS, &updateTS, &labels, &annotations)
	if err != nil {
		tx2.Rollback()
		t.Fatalf("failed to query target user metadata: %v", err)
	}

	tx2.Rollback()

	err = executeAsUser(t, adminDB, adminUserID, func(tx *sql.Tx) error {
		nameVal := ""
		if name.Valid {
			nameVal = name.String
		}
		displayNameVal := sql.NullString{Valid: displayName.Valid, String: displayName.String}
		workspaceVal := sql.NullString{Valid: workspace.Valid, String: workspace.String}

		var dnStr, wsStr string
		if displayNameVal.Valid {
			dnStr = displayNameVal.String
		}
		if workspaceVal.Valid {
			wsStr = workspaceVal.String
		}

		result, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW($1, NULLIF($2, ''), NULLIF($3, ''), CURRENT_TIMESTAMP, $4, $5, $6::json, $7::json)::api.metadata
			WHERE id = $8
		`, nameVal, dnStr, wsStr, creationTS, updateTS, labels, annotations, targetUserID)
		if err != nil {
			return err
		}

		rows, _ := result.RowsAffected()
		if rows != 1 {
			t.Fatalf("expected 1 row updated (soft deleted), got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("admin can soft delete other users")
}

func TestUserProfileRBAC_AdminCannotDeleteSelf(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	adminUserID := createUserWithPermissions(t, tx1, "admin-noself", "adminnoself@test.local",
		[]string{"user_profile:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, adminUserID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW((metadata).name, (metadata).display_name, (metadata).workspace, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, (metadata).labels, (metadata).annotations)::api.metadata
			WHERE id = $1
		`, adminUserID)

		if err == nil {
			t.Fatalf("expected error when admin tries to delete self, got nil")
		}

		if !strings.Contains(err.Error(), "Cannot delete your own user profile") {
			t.Fatalf("expected 'Cannot delete your own user profile' error, got: %v", err)
		}

		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("admin cannot delete self (trigger blocked)")
}

func TestUserProfileRBAC_CannotSoftDeleteAdminUser(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	deleterID := createUserWithPermissions(t, tx1, "admin-user-deleter", "adminuserdeleter@test.local",
		[]string{"user_profile:delete", "user_profile:read"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, deleterID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.user_profiles
			SET metadata = ROW((metadata).name, (metadata).display_name, (metadata).workspace, CURRENT_TIMESTAMP, (metadata).creation_timestamp, CURRENT_TIMESTAMP, (metadata).labels, (metadata).annotations)::api.metadata
			WHERE (metadata).name = 'admin'
		`)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS error: admin user should not be soft deletable")
	}
	t.Logf("admin user soft delete blocked by RLS: %v", err)
}

func TestUserProfileRBAC_CannotHardDeleteAdminUser(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	deleterID := createUserWithPermissions(t, tx1, "admin-user-deleter2", "adminuserdeleter2@test.local",
		[]string{"user_profile:delete", "user_profile:read"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	err = executeAsUser(t, adminDB, deleterID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			DELETE FROM api.user_profiles
			WHERE (metadata).name = 'admin'
		`)
		if err != nil {
			return err
		}
		rows, _ := result.RowsAffected()
		if rows != 0 {
			t.Fatalf("expected 0 rows deleted, got %d", rows)
		}
		return nil
	})

	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	t.Logf("admin user hard delete blocked by RLS (0 rows affected)")
}
