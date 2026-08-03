package dbtest

import (
	"context"
	"database/sql"
	"strconv"

	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// newAlias builds a ModelAlias with alias_normalized derived the way the
// application derives it, so these tests exercise the Go rule and the database
// index together rather than one in isolation. Workspace is left unset on
// purpose: the database derives it from the registry.
func newAlias(registryID int, model, version, alias string) *v1.ModelAlias {
	return &v1.ModelAlias{
		ModelRegistryID: registryID,
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

// TestModelAliasUniqueWithinRegistry covers the constraint the table exists for:
// an alias is unique inside one registry, compared on the normalized form, and
// two registries are free to use the same alias.
func TestModelAliasUniqueWithinRegistry(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryA := createTestModelRegistry(t, db, s, "alias-unique-a")
	registryB := createTestModelRegistry(t, db, s, "alias-unique-b")

	require.NoError(t, s.CreateModelAlias(newAlias(registryA, "qwen3", "v1", "Qwen3")))

	err := s.CreateModelAlias(newAlias(registryA, "llama", "v1", "qwen3"))
	require.Error(t, err, "a second alias normalizing to qwen3 must be rejected by the unique index")

	// The same alias in a different registry is fine: uniqueness is per registry.
	require.NoError(t, s.CreateModelAlias(newAlias(registryB, "qwen3", "v1", "Qwen3")))

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

	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v1", "Qwen3")))

	variants := []struct {
		name  string
		alias string
	}{
		{"identical", "Qwen3"},
		{"lowercase", "qwen3"},
		{"uppercase", "QWEN3"},
		{"surrounding whitespace", " Qwen3 "},
		{"no-break space (NFKC folds it to a trimmable space)", "\u00a0Qwen3\u00a0"},
		{"fullwidth (NFKC)", "\uff31\uff57\uff45\uff4e\uff13"},
	}

	for _, tt := range variants {
		t.Run(tt.name, func(t *testing.T) {
			assert.Error(t, s.CreateModelAlias(newAlias(registryID, "llama", "v1", tt.alias)),
				"alias %q normalizes to %q and must collide", tt.alias, v1.NormalizeModelAlias(tt.alias))
		})
	}

	// Inner spacing is part of the display name, so this one is a different
	// alias. It goes on a different model version because a model version
	// carries at most one alias.
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v2", "Qwen 3")))
	assert.Len(t, aliasesOfRegistry(t, s, registryID), 2)
}

// TestModelAliasOnePerModelVersion: the requirement is a single display name per
// model, so a second alias for the same model version is rejected. Without this
// the read path -- which joins aliases onto the live model list -- would have no
// defined winner.
func TestModelAliasOnePerModelVersion(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryID := createTestModelRegistry(t, db, s, "alias-one-per-model")

	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v1", "Qwen3")))

	assert.Error(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v1", "Qwen3 Chat")),
		"a model version must not accumulate a second alias")

	// A different version of the same model is a different model, so it may
	// carry its own alias.
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v2", "Qwen3 Chat")))
	assert.Len(t, aliasesOfRegistry(t, s, registryID), 2)
}

// TestModelAliasWorkspaceIsDerivedFromRegistry: the workspace column must not be
// something a client can choose. It is denormalized for reads, but a write that
// names a different workspace has that value discarded and replaced with the
// owning registry's -- so it cannot be used to plant a row that authorizes
// against one workspace while occupying a unique-index slot in another.
func TestModelAliasWorkspaceIsDerivedFromRegistry(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)
	ctx := context.Background()

	registryID := createTestModelRegistry(t, db, s, "alias-workspace-derived")

	// Straight SQL rather than the Go client: pkg/storage strips Workspace
	// before sending, but the guarantee has to hold for anyone talking to
	// PostgREST directly, which is the case that actually matters.
	_, err := db.ExecContext(ctx, `
		INSERT INTO api.model_aliases (model_registry_id, workspace, model_name, model_version, alias, alias_normalized)
		VALUES ($1, 'someone-elses-workspace', 'qwen3', 'v1', 'Qwen3', 'qwen3')`, registryID)
	require.NoError(t, err)

	stored := aliasesOfRegistry(t, s, registryID)
	require.Len(t, stored, 1)
	assert.Equal(t, "default", stored[0].Workspace, "the registry's workspace wins over the one supplied by the client")

	// The same on update.
	_, err = db.ExecContext(ctx,
		`UPDATE api.model_aliases SET workspace = 'someone-elses-workspace' WHERE id = $1`, stored[0].ID)
	require.NoError(t, err)

	var workspace string
	require.NoError(t, db.QueryRowContext(ctx,
		`SELECT workspace FROM api.model_aliases WHERE id = $1`, stored[0].ID).Scan(&workspace))
	assert.Equal(t, "default", workspace, "an update cannot move a row into another workspace either")

	// And the storage layer does not present the field as writable in the first place.
	viaClient := newAlias(registryID, "llama", "v1", "Llama")
	viaClient.Workspace = "someone-elses-workspace"
	require.NoError(t, s.CreateModelAlias(viaClient))

	after := aliasesOfRegistry(t, s, registryID)
	require.Len(t, after, 2)

	for _, a := range after {
		assert.Equal(t, "default", a.Workspace)
	}
}

// TestModelAliasCascadeOnRegistryDelete: the foreign key removes a whole class
// of orphan rows when a registry goes away.
func TestModelAliasCascadeOnRegistryDelete(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	registryID := createTestModelRegistry(t, db, s, "alias-cascade")

	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "qwen3", "v1", "Qwen3")))
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "llama", "v1", "Llama")))
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
	require.NoError(t, s.CreateModelAlias(newAlias(registryID, "removed-model", "v1", "Qwen3")))

	existing := aliasesOfRegistry(t, s, registryID)
	require.Len(t, existing, 1)

	takeover := newAlias(registryID, "qwen3", "v2", "Qwen3")
	require.NoError(t, s.UpdateModelAlias(strconv.Itoa(existing[0].ID), takeover),
		"an orphaned row must not block a new alias with the same normalized form")

	after := aliasesOfRegistry(t, s, registryID)
	require.Len(t, after, 1, "the takeover overwrites the orphan instead of adding a row")
	assert.Equal(t, "qwen3", after[0].ModelName)
	assert.Equal(t, "v2", after[0].ModelVersion)
	assert.Equal(t, "Qwen3", after[0].Alias)
}

// TestModelAliasRLS checks the policies shipped with the table: reads need
// model:read and writes need model:push. Both actions already exist (042); the
// table adds no new permission action.
//
// The workspace the policies pass to api.has_permission is the owning
// registry's, looked up through public.model_registry_workspace, never the row's
// own column -- see TestModelAliasWorkspaceIsDerivedFromRegistry for why the
// column cannot be trusted.
//
// Only the action half of the policies is asserted here. This schema caps a
// deployment at one workspace (021), so api.has_permission does not consume the
// workspace argument and a trigger rejects workspace-scoped role assignments
// outright (error 10041) -- there is no second workspace for a caller to be
// refused from. That the policies nevertheless hand it the registry's real
// workspace is what TestModelAliasWorkspaceIsDerivedFromRegistry pins down.
func TestModelAliasRLS(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)
	ctx := context.Background()

	registryID := createTestModelRegistry(t, db, s, "alias-rls")

	tx, err := db.BeginTx(ctx, nil)
	require.NoError(t, err)

	reader := createUserWithPermissions(t, tx, "alias-reader", "alias-reader@example.com", []string{"model:read"})
	pusher := createUserWithPermissions(t, tx, "alias-pusher", "alias-pusher@example.com", []string{"model:read", "model:push"})
	stranger := createUserWithPermissions(t, tx, "alias-stranger", "alias-stranger@example.com", []string{"cluster:read"})

	require.NoError(t, tx.Commit())

	t.Cleanup(func() {
		_, _ = db.ExecContext(ctx, `DELETE FROM api.role_assignments WHERE (spec).user_id IN ($1::uuid, $2::uuid, $3::uuid)`, reader, pusher, stranger)
		_, _ = db.ExecContext(ctx, `DELETE FROM api.roles WHERE (metadata).name IN ('alias-reader-role', 'alias-pusher-role', 'alias-stranger-role')`)
	})

	// executeAsUser never commits, so a write here is only ever an
	// allowed/denied probe. The row the read probes look for is seeded on the
	// admin connection below, which is superuser and bypasses RLS.
	// workspace is omitted: the trigger derives it. Each probe uses its own model
	// version because a model version carries at most one alias, and a
	// unique-index violation would otherwise be mistaken for an RLS refusal.
	insert := func(userID, model, alias string) error {
		return executeAsUser(t, db, userID, func(tx *sql.Tx) error {
			_, err := tx.ExecContext(ctx, `
				INSERT INTO api.model_aliases (model_registry_id, model_name, model_version, alias, alias_normalized)
				VALUES ($1, $2, 'v1', $3, $4)`,
				registryID, model, alias, v1.NormalizeModelAlias(alias))

			return err
		})
	}

	_, err = db.ExecContext(ctx, `
		INSERT INTO api.model_aliases (model_registry_id, model_name, model_version, alias, alias_normalized)
		VALUES ($1, 'qwen3', 'v1', 'Qwen3', 'qwen3')`, registryID)
	require.NoError(t, err)

	countVisible := func(userID string) int {
		var n int

		require.NoError(t, executeAsUser(t, db, userID, func(tx *sql.Tx) error {
			return tx.QueryRowContext(ctx,
				`SELECT count(*) FROM api.model_aliases WHERE model_registry_id = $1`, registryID).Scan(&n)
		}))

		return n
	}

	t.Run("model:push may write", func(t *testing.T) {
		require.NoError(t, insert(pusher, "pusher-model", "PusherAlias"))
	})

	t.Run("model:read alone may not write", func(t *testing.T) {
		assert.Error(t, insert(reader, "reader-model", "ReadOnlyAttempt"))
	})

	t.Run("no model permission may not write", func(t *testing.T) {
		assert.Error(t, insert(stranger, "stranger-model", "StrangerAttempt"))
	})

	t.Run("model:read may read", func(t *testing.T) {
		assert.Equal(t, 1, countVisible(reader))
	})

	t.Run("no model permission sees nothing", func(t *testing.T) {
		assert.Equal(t, 0, countVisible(stranger))
	})
}
