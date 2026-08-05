package dbtest

import (
	"context"
	"database/sql"
	"strings"
	"testing"
)

// TestCreateApiKeyRequiresWorkspace covers 079_api_key_workspace_required.
//
// An API key is always scoped to one workspace, but p_workspace used to accept
// null: nothing rejected it and the resulting key had a null metadata.workspace.
// The enterprise api.has_permission treats such a key as invalid and fails
// closed, so the key silently loses access to everything. Reject it up front.
func TestCreateApiKeyRequiresWorkspace(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	user := CreateTestUser(t, "wsrequired", "wsrequired@example.com", "testpassword")

	rejected := []struct {
		name      string
		workspace *string
	}{
		{name: "null workspace", workspace: nil},
		{name: "empty workspace", workspace: strPtr("")},
	}

	for _, tt := range rejected {
		t.Run(tt.name, func(t *testing.T) {
			var id string

			err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
				return tx.QueryRowContext(ctx, `
					SELECT id FROM api.create_api_key(
						p_workspace := $1,
						p_name := 'ws-required-key',
						p_quota := 0
					)`, tt.workspace).Scan(&id)
			})
			if err == nil {
				_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", id)
				t.Fatal("create_api_key accepted a key without a workspace, want rejection")
			}

			if !strings.Contains(err.Error(), "workspace is required") {
				t.Fatalf("create_api_key failed for the wrong reason: %v", err)
			}
		})
	}

	t.Run("workspace is still accepted", func(t *testing.T) {
		var id, workspace string

		err := execWithContext(t, db, []SetContextFunc{setUserContext(user.ID), setJwtSecretContext()}, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx, `
				SELECT id, (metadata).workspace FROM api.create_api_key(
					p_workspace := 'default',
					p_name := 'ws-required-ok',
					p_quota := 0
				)`).Scan(&id, &workspace)
		})
		if err != nil {
			t.Fatalf("create_api_key with a workspace: %v", err)
		}

		t.Cleanup(func() {
			_, _ = db.ExecContext(ctx, "DELETE FROM api.api_keys WHERE id = $1", id)
		})

		if workspace != "default" {
			t.Errorf("metadata.workspace = %q, want %q", workspace, "default")
		}
	})
}

func strPtr(s string) *string { return &s }
