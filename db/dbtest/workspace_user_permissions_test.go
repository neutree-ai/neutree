package dbtest

import (
	"context"
	"testing"

	"github.com/lib/pq"
)

func TestWorkspaceUserPermissions_HasExpectedPermissions(t *testing.T) {
	db := GetTestDB(t)
	ctx := context.Background()

	expectedPermissions := []string{
		"workspace:read",
		"endpoint:read",
		"endpoint:create",
		"endpoint:update",
		"endpoint:delete",
		"image_registry:read",
		"image_registry:create",
		"image_registry:update",
		"image_registry:delete",
		"model_registry:read",
		"model_registry:create",
		"model_registry:update",
		"model_registry:delete",
		"model:read",
		"model:push",
		"model:pull",
		"model:delete",
		"engine:read",
		"engine:create",
		"engine:update",
		"engine:delete",
		"cluster:read",
		"cluster:create",
		"cluster:update",
		"cluster:delete",
		"model_catalog:read",
		"model_catalog:create",
		"model_catalog:update",
		"model_catalog:delete",
	}

	var permissions []string
	err := db.QueryRowContext(ctx, `
		SELECT (spec).permissions
		FROM api.roles
		WHERE (metadata).name = 'workspace-user'
	`).Scan(pq.Array(&permissions))
	if err != nil {
		t.Fatalf("failed to query workspace-user permissions: %v", err)
	}

	// Check that all expected permissions are present
	permSet := make(map[string]bool)
	for _, p := range permissions {
		permSet[p] = true
	}

	for _, expected := range expectedPermissions {
		if !permSet[expected] {
			t.Errorf("missing expected permission: %s", expected)
		}
	}

	// Check that no unexpected permissions are present
	expectedSet := make(map[string]bool)
	for _, p := range expectedPermissions {
		expectedSet[p] = true
	}

	for _, p := range permissions {
		if !expectedSet[p] {
			t.Errorf("unexpected permission found: %s", p)
		}
	}

	t.Logf("workspace-user has %d permissions", len(permissions))
}
