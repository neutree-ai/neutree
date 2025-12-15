package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

func TestDefaultWorkspaceProtection_CannotDeleteDefaultWorkspace(t *testing.T) {
	adminDB := GetTestDB(t)
	ctx := context.Background()

	tx, err := adminDB.BeginTx(ctx, nil)
	if err != nil {
		t.Fatalf("failed to begin transaction: %v", err)
	}

	userID := createUserWithPermissions(t, tx, "delete-user", "delete@example.com", []string{"workspace:read", "workspace:delete"})
	if err = tx.Commit(); err != nil {
		t.Fatalf("failed to commit: %v", err)
	}

	// Test: delete default workspace should be blocked
	err = executeAsUser(t, adminDB, userID, func(tx *sql.Tx) error {
		_, err := tx.ExecContext(ctx, `
			UPDATE api.workspaces
			SET metadata.deletion_timestamp = now()
			WHERE (metadata).name = 'default'
		`)
		if err != nil {
			return err
		}

		return nil
	})

	if err == nil {
		t.Fatalf("expected error when deleting default workspace, got nil")
	}

	// Check that the error message contains the correct error code
	if !strings.Contains(err.Error(), `"code": "10043"`) {
		t.Fatalf("expected error code 10043, got: %v", err)
	}

	t.Logf("protection correctly blocked delete default workspace with error code 10043: %v", err)
}
