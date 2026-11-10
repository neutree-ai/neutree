package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// ===============================================
// Helper Functions for DRY Test Code
// ===============================================

// createImageRegistry creates an image registry and returns its ID
func createImageRegistry(t *testing.T, tx *sql.Tx, name, workspace string) int {
	t.Helper()
	var registryID int
	err := tx.QueryRowContext(context.Background(), `
		INSERT INTO api.image_registries (api_version, kind, spec, metadata)
		VALUES (
			'v1',
			'ImageRegistry',
			ROW('https://registry.example.com', 'my-repo', '{}'::json)::api.image_registry_spec,
			ROW($1, NULL, $2, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata
		)
		RETURNING id
	`, name, workspace).Scan(&registryID)
	if err != nil {
		t.Fatalf("failed to create image registry: %v", err)
	}
	return registryID
}

// createUserWithPermissions creates a user with specified permissions and returns user ID
func createUserWithPermissions(t *testing.T, tx *sql.Tx, username, email string, permissions []string) string {
	t.Helper()
	ctx := context.Background()

	// Create user via GoTrue
	user := CreateTestUser(t, username, email, "password123")

	// Create role with specified permissions
	roleName := username + "-role"
	permissionsArray := "ARRAY['" + strings.Join(permissions, "','") + "']::api.permission_action[]"

	_, err := tx.ExecContext(ctx, `
		INSERT INTO api.roles (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'Role',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW(NULL, `+permissionsArray+`)::api.role_spec
		)
	`, roleName)
	if err != nil {
		t.Fatalf("failed to create role: %v", err)
	}

	// Assign role to user globally
	_, err = tx.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, NULL, TRUE, $3, NULL, NULL, NULL, NULL)::api.role_assignment_spec
		)
	`, username+"-role-assignment", user.ID, roleName)
	if err != nil {
		t.Fatalf("failed to create role assignment: %v", err)
	}

	return user.ID
}

// roleAssignment is a row seedRoleAssignment stores. Empty strings are NULL.
type roleAssignment struct {
	Name        string // newOrgName("ra") when empty
	UserID      string
	Workspace   string
	Global      bool
	Role        string
	OrgUnit     string
	SubjectKind string
	SubjectName string
	Team        string
	// Replica bypasses the triggers, so the write checks do not apply.
	Replica bool
}

// rowExecer is a *sql.DB or a *sql.Tx.
type rowExecer interface {
	Exec(query string, args ...any) (sql.Result, error)
}

// insertRoleAssignment stores ra as the caller of ex and returns its name.
func insertRoleAssignment(ex rowExecer, ra roleAssignment) (string, error) {
	if ra.Name == "" {
		ra.Name = newOrgName("ra")
	}

	if ra.Replica {
		if _, err := ex.Exec(`SET LOCAL session_replication_role = replica`); err != nil {
			return ra.Name, err
		}
	}

	_, err := ex.Exec(`
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES ('v1', 'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, $3, $4, $5, $6, $7, $8, $9)::api.role_assignment_spec)`,
		ra.Name, nullIfEmpty(ra.UserID), nullIfEmpty(ra.Workspace), ra.Global, ra.Role,
		nullIfEmpty(ra.OrgUnit), nullIfEmpty(ra.SubjectKind), nullIfEmpty(ra.SubjectName), nullIfEmpty(ra.Team))

	return ra.Name, err
}

// seedRoleAssignment stores ra as postgres, in a transaction of its own, and
// returns its name.
func seedRoleAssignment(t *testing.T, db *sql.DB, ra roleAssignment) string {
	t.Helper()

	var name string
	if err := execWithContext(t, db, nil, func(tx *sql.Tx) error {
		var err error
		name, err = insertRoleAssignment(tx, ra)
		return err
	}); err != nil {
		t.Fatalf("seeding role assignment %+v failed: %v", ra, err)
	}

	return name
}

// executeAsUser executes a function within a transaction as the specified user
func executeAsUser(t *testing.T, db *sql.DB, userID string, fn func(*sql.Tx) error) error {
	t.Helper()
	ctx := context.Background()

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}
	defer func() {
		_ = tx.Rollback()
	}()

	// Set role to api_user to enable RLS
	_, err = tx.ExecContext(ctx, "SET LOCAL ROLE api_user")
	if err != nil {
		t.Fatalf("failed to set role: %v", err)
	}

	// Set user context
	_, err = tx.ExecContext(ctx, "SET LOCAL request.jwt.claim.sub = '"+userID+"'")
	if err != nil {
		t.Fatalf("failed to set user context: %v", err)
	}

	return fn(tx)
}

// ===============================================
// Test Cases
// ===============================================

func TestSoftDelete_WithoutDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with only read permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-without-delete", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "reader-user", "reader@example.com", []string{"image_registry:read"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: try to soft delete without delete permission
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now()
			WHERE id = $1
		`, registryID)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS policy error: user without delete permission should not be able to soft delete")
	}
	t.Logf("soft delete blocked by RLS: %v", err)
}

func TestSoftDelete_WithDeletePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with delete permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-with-delete", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "deleter-user", "deleter@example.com",
		[]string{"image_registry:read", "image_registry:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: soft delete should succeed
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now()
			WHERE id = $1
		`, registryID)
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
		t.Fatalf("soft delete should succeed: %v", err)
	}
	t.Logf("soft delete succeeded")
}

func TestSoftDelete_CannotModifySpec(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with delete permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-modify-spec", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "deleter-user2", "deleter2@example.com",
		[]string{"image_registry:read", "image_registry:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: try to modify spec during soft delete
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now(),
			    spec.url = 'https://new-registry.example.com'
			WHERE id = $1
		`, registryID)
		return err
	})

	if err == nil {
		t.Fatal("expected trigger error: cannot modify spec during soft delete")
	}
	if !strings.Contains(err.Error(), "Cannot modify spec during soft delete") {
		t.Fatalf("expected spec modification error, got: %v", err)
	}
	t.Logf("spec modification blocked by trigger: %v", err)
}

// Note: image_registries table doesn't have a status field, so we skip this test
// The trigger logic handles status validation for resources that do have status fields

func TestUpdate_WithUpdatePermission(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with update permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-update-perm", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "updater-user", "updater@example.com",
		[]string{"image_registry:read", "image_registry:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: normal update should succeed
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET spec.url = 'https://updated-registry.example.com'
			WHERE id = $1
		`, registryID)
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
		t.Fatalf("update should succeed: %v", err)
	}
	t.Logf("update succeeded")
}

func TestUpdate_CannotSoftDelete(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with only update permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-cannot-delete", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "updater-user2", "updater2@example.com",
		[]string{"image_registry:read", "image_registry:update"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: try to soft delete with only update permission
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now()
			WHERE id = $1
		`, registryID)
		return err
	})

	if err == nil {
		t.Fatal("expected RLS policy error: user with only update permission should not be able to soft delete")
	}
	t.Logf("soft delete blocked by RLS: %v", err)
}

func TestSoftDelete_AlreadyDeleted(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	// Setup: create image registry and user with delete permission
	tx1, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	registryID := createImageRegistry(t, tx1, "registry-already-deleted", "test-workspace")
	userID := createUserWithPermissions(t, tx1, "deleter-user3", "deleter3@example.com",
		[]string{"image_registry:read", "image_registry:delete"})

	if err = tx1.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// First soft delete
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now()
			WHERE id = $1
		`, registryID)
		return err
	})
	if err != nil {
		t.Fatalf("first soft delete failed: %v", err)
	}

	// Test: update deletion_timestamp again (should succeed as idempotent operation)
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		result, err := tx.ExecContext(ctx, `
			UPDATE api.image_registries
			SET metadata.deletion_timestamp = now()
			WHERE id = $1
		`, registryID)
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
		t.Fatalf("updating already deleted resource should succeed: %v", err)
	}
	t.Logf("updating already deleted resource succeeded (idempotent)")
}
