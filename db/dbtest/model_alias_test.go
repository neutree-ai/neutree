package dbtest

import (
	"context"
	"database/sql"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// newAlias builds a ModelAlias with alias_normalized derived the way the
// application derives it, so these tests exercise the Go rule and the database
// index together rather than one in isolation.
func newAlias(registryID int, workspace, model, version, alias string) *v1.ModelAlias {
	return &v1.ModelAlias{
		ModelRegistryID: registryID,
		Workspace:       workspace,
		ModelName:       model,
		ModelVersion:    version,
		Alias:           alias,
		AliasNormalized: v1.NormalizeModelAlias(alias),
	}
}

func aliasesOfRegistry(t *testing.T, s storage.Storage, registryID int) []v1.ModelAlias {
	t.Helper()

	got, err := s.ListModelAlias(storage.ListOption{
		Filters: []storage.Filter{{Column: "model_registry_id", Operator: "eq", Value: strconv.Itoa(registryID)}},
	})
	require.NoError(t, err)

	return got
}

// createUserInWorkspace creates a user holding permissions in one workspace
// only, so the tests can tell a permission check from a workspace check.
func createUserInWorkspace(t *testing.T, tx *sql.Tx, username, email, workspace string, permissions []string) string {
	t.Helper()
	ctx := context.Background()

	user := CreateTestUser(t, username, email, "password123")

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
		t.Fatalf("failed to create role %s: %v", roleName, err)
	}

	_, err = tx.ExecContext(ctx, `
		INSERT INTO api.role_assignments (api_version, kind, metadata, spec)
		VALUES (
			'v1',
			'RoleAssignment',
			ROW($1, NULL, NULL, NULL, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, '{}'::json, '{}'::json)::api.metadata,
			ROW($2::uuid, $3, FALSE, $4)::api.role_assignment_spec
		)
	`, username+"-role-assignment", user.ID, workspace, roleName)
	if err != nil {
		t.Fatalf("failed to assign role %s in workspace %s: %v", roleName, workspace, err)
	}

	return user.ID
}

// TestModelAliasUniqueWithinRegistry covers the constraint the table exists for:
// an alias is unique inside one registry, compared on the normalized form, and
// two registries are free to use the same alias.
func TestModelAliasUniqueWithinRegistry(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryA := createTestModelRegistry(t, db, s, "alias-unique-a")
	registryB := createTestModelRegistry(t, db, s, "alias-unique-b")

	require.NoError(t, s.CreateModelAlias(newAlias(registryA, "default", "qwen3", "v1", "Qwen3")))

	err := s.CreateModelAlias(newAlias(registryA, "default", "llama", "v1", "qwen3"))
	require.Error(t, err, "a second alias normalizing to qwen3 must be rejected by the unique index")

	// The same alias in a different registry is fine: uniqueness is per registry.
	require.NoError(t, s.CreateModelAlias(newAlias(registryB, "default", "qwen3", "v1", "Qwen3")))

	assert.Len(t, aliasesOfRegistry(t, s, registryA), 1)
	assert.Len(t, aliasesOfRegistry(t, s, registryB), 1)
}

// TestModelAliasNormalizationVariantsCollide is the case-, whitespace- and
// NFKC-insensitivity requirement: every spelling below has to collide with the
// alias already stored.
func TestModelAliasNormalizationVariantsCollide(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryID := createTestModelRegistry(t, db, s, "alias-normalization")

	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "default", "qwen3", "v1", "Qwen3")))

	variants := []struct {
		name  string
		alias string
	}{
		{"identical", "Qwen3"},
		{"lowercase", "qwen3"},
		{"uppercase", "QWEN3"},
		{"surrounding whitespace", " Qwen3 "},
		{"no-break space (NFKC folds it to a trimmable space)", " Qwen3 "},
		{"fullwidth (NFKC)", "Ｑｗｅｎ３"},
	}

	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, s.CreateModelAlias(newAlias(registryID, "default", "llama", "v1", tt.alias)),
				"alias %q normalizes to %q and must collide", tt.alias, v1.NormalizeModelAlias(tt.alias))
		})
	}

	// Inner spacing is part of the display name, so this one is a different alias.
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "default", "qwen3", "v1", "Qwen 3")))
	assert.Len(t, aliasesOfRegistry(t, s, registryID), 2)
}

// TestModelAliasCascadeOnRegistryDelete: the foreign key removes a whole class
// of orphan rows when a registry goes away.
func TestModelAliasCascadeOnRegistryDelete(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryID := createTestModelRegistry(t, db, s, "alias-cascade")

	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "default", "qwen3", "v1", "Qwen3")))
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "default", "llama", "v1", "Llama")))
	require.Len(t, aliasesOfRegistry(t, s, registryID), 2)

	_, err := db.Exec("DELETE FROM api.model_registries WHERE id = $1", registryID)
	require.NoError(t, err)

	assert.Empty(t, aliasesOfRegistry(t, s, registryID), "aliases must go with their registry")
}

// TestModelAliasOrphanRowCanBeTakenOver: the table is a projection of the
// registry filesystem, so a row whose model was removed out of band must not
// reserve its alias forever. Repointing it at a live model is the takeover.
func TestModelAliasOrphanRowCanBeTakenOver(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryID := createTestModelRegistry(t, db, s, "alias-orphan")

	// "removed-model" is gone from the registry as far as this test is
	// concerned; the alias row is the leftover.
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "default", "removed-model", "v1", "Qwen3")))

	existing := aliasesOfRegistry(t, s, registryID)
	require.Len(t, existing, 1)

	takeover := newAlias(registryID, "default", "qwen3", "v2", "Qwen3")
	require.NoError(t, s.UpdateModelAlias(strconv.Itoa(existing[0].ID), takeover),
		"an orphaned row must not block a new alias with the same normalized form")

	after := aliasesOfRegistry(t, s, registryID)
	require.Len(t, after, 1, "the takeover overwrites the orphan instead of adding a row")
	assert.Equal(t, "qwen3", after[0].ModelName)
	assert.Equal(t, "v2", after[0].ModelVersion)
	assert.Equal(t, "Qwen3", after[0].Alias)
}

// TestModelAliasRLS checks the policies added with the table: reads need
// model:read and writes need model:push, both scoped by the row's workspace.
// The permission actions themselves already exist (042); no new action is added.
func TestModelAliasRLS(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)
	ctx := context.Background()

	const (
		workspace      = "alias-rls-ws"
		otherWorkspace = "alias-rls-other-ws"
	)

	registryID := createTestModelRegistryInWorkspace(t, db, s, "alias-rls", workspace)

	var (
		reader   string // model:read in workspace
		pusher   string // model:read + model:push in workspace
		outsider string // model:read + model:push, but in another workspace
	)

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)

	reader = createUserInWorkspace(t, tx, "alias-reader", "alias-reader@example.com", workspace, []string{"model:read"})
	pusher = createUserInWorkspace(t, tx, "alias-pusher", "alias-pusher@example.com", workspace, []string{"model:read", "model:push"})
	outsider = createUserInWorkspace(t, tx, "alias-outsider", "alias-outsider@example.com", otherWorkspace, []string{"model:read", "model:push"})

	require.NoError(t, tx.Commit())

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid, $3::uuid)`, reader, pusher, outsider)
		_, _ = db.ExecContext(ctx, `DELETE FROM api.roles WHERE (metadata).name LIKE 'alias-%-role'`)
	})

	insert := func(userID, alias string) error {
		return executeAsUser(t, db, userID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO api.model_aliases (model_registry_id, workspace, model_name, model_version, alias, alias_normalized)
				VALUES ($1, $2, 'qwen3', 'v1', $3, $4)`,
				registryID, workspace, alias, v1.NormalizeModelAlias(alias))

			return err
		})
	}

	countVisible := func(userID string) int {
		var n int

		require.NoError(t, executeAsUser(t, db, userID, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				`SELECT count(*) FROM api.model_aliases WHERE model_registry_id = $1`, registryID).Scan(&n)
		}))

		return n
	}

	t.Run("model:push may write", func(t *testing.T) {
		require.NoError(t, insert(pusher, "Qwen3"))
	})

	t.Run("model:read alone may not write", func(t *testing.T) {
		assert.Error(t, insert(reader, "ReadOnlyAttempt"))
	})

	t.Run("another workspace may not write here", func(t *testing.T) {
		assert.Error(t, insert(outsider, "OutsiderAttempt"))
	})

	t.Run("model:read may read", func(t *testing.T) {
		assert.Equal(t, 1, countVisible(reader))
	})

	t.Run("another workspace sees nothing", func(t *testing.T) {
		assert.Equal(t, 0, countVisible(outsider))
	})
}
