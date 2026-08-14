package dbtest

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// A write may not leave a model catalog with a spec that declares neither a
// model nor variants. These go through PostgREST rather than plain SQL because
// the destructive shape only exists there: a PATCH replaces the whole composite
// spec column, so an empty spec in the payload blanks every attribute.

// createTestModelCatalog stores a valid ordinary catalog and returns its id.
func createTestModelCatalog(t *testing.T, db *sql.DB, s storage.Storage, name string) int {
	t.Helper()

	require.NoError(t, s.CreateModelCatalog(&v1.ModelCatalog{
		APIVersion: "v1",
		Kind:       "ModelCatalog",
		Metadata:   &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.ModelCatalogSpec{
			Model:  &v1.ModelSpec{Registry: "huggingface", Name: "org/" + name},
			Engine: &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.9.1"},
		},
	}))

	var id int
	require.NoError(t, db.QueryRow(
		"SELECT id FROM api.model_catalogs WHERE (metadata).name = $1 AND (metadata).workspace = 'default'",
		name,
	).Scan(&id))

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM api.model_catalogs WHERE id = $1", id)
	})

	return id
}

func modelNameOf(t *testing.T, s storage.Storage, id int) string {
	t.Helper()

	got, err := s.GetModelCatalog(strconv.Itoa(id))
	require.NoError(t, err)
	require.NotNil(t, got.Spec)
	require.NotNil(t, got.Spec.Model)

	return got.Spec.Model.Name
}

// TestModelCatalogRejectsEmptySpec is the regression for the reported bug: an
// edit carrying an empty spec reported success and blanked the stored recipe.
func TestModelCatalogRejectsEmptySpec(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelCatalog(t, db, s, "mc-empty-spec")
	before := modelNameOf(t, s, id)

	err := s.UpdateModelCatalog(strconv.Itoa(id), &v1.ModelCatalog{Spec: &v1.ModelCatalogSpec{}})
	require.Error(t, err, "an update to an empty spec must be rejected")

	assert.Equal(t, before, modelNameOf(t, s, id), "the rejected update must not have changed the spec")
}

func TestModelCatalogRejectsEmptySpecOnCreate(t *testing.T) {
	s := NewTestStorage(t)

	err := s.CreateModelCatalog(&v1.ModelCatalog{
		APIVersion: "v1",
		Kind:       "ModelCatalog",
		Metadata:   &v1.Metadata{Name: "mc-empty-spec-create", Workspace: "default"},
		Spec:       &v1.ModelCatalogSpec{},
	})
	require.Error(t, err, "a catalog with an empty spec must not be creatable")
}

// TestModelCatalogRejectsNullSpec covers the other blanking shape: `spec: null`
// sets the whole composite column to NULL rather than to a row of NULLs.
func TestModelCatalogRejectsNullSpec(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelCatalog(t, db, s, "mc-null-spec")

	_, err := db.Exec("UPDATE api.model_catalogs SET spec = NULL WHERE id = $1", id)
	require.Error(t, err, "nulling the spec column must be rejected")

	assert.Equal(t, "org/mc-null-spec", modelNameOf(t, s, id))
}

// TestModelCatalogAllowsRecipeSpec guards the false positive: a recipe catalog
// declares no top-level model, only variants, and must stay writable.
func TestModelCatalogAllowsRecipeSpec(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	require.NoError(t, s.CreateModelCatalog(&v1.ModelCatalog{
		APIVersion: "v1",
		Kind:       "ModelCatalog",
		Metadata:   &v1.Metadata{Name: "mc-recipe", Workspace: "default"},
		Spec: &v1.ModelCatalogSpec{
			Engine:   &v1.EndpointEngineSpec{Engine: "vllm", Version: "v0.9.1"},
			Variants: map[string]v1.RecipeVariant{"default": {Model: &v1.ModelSpec{Name: "org/model"}}},
		},
	}))

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM api.model_catalogs WHERE (metadata).name = 'mc-recipe'")
	})
}

// TestModelCatalogAllowsStatusOnlyUpdate guards the controller's hot path: it
// writes status without a spec, which must not trip the guard.
func TestModelCatalogAllowsStatusOnlyUpdate(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelCatalog(t, db, s, "mc-status-only")

	require.NoError(t, s.UpdateModelCatalog(strconv.Itoa(id), &v1.ModelCatalog{
		Status: &v1.ModelCatalogStatus{Phase: v1.ModelCatalogPhaseREADY},
	}))

	got, err := s.GetModelCatalog(strconv.Itoa(id))
	require.NoError(t, err)
	assert.Equal(t, v1.ModelCatalogPhaseREADY, got.Status.Phase)
	assert.Equal(t, "org/mc-status-only", modelNameOf(t, s, id))
}

// TestModelCatalogAlreadyEmptySpecStaysWritable covers rows blanked before the
// guard existed. Freezing them would be worse than the bug: the controller
// could no longer write status, so they could never be reconciled or repaired.
func TestModelCatalogAlreadyEmptySpecStaysWritable(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	// Simulate pre-migration data: the guard has to be off to produce a row it
	// would otherwise reject.
	_, err := db.Exec("ALTER TABLE api.model_catalogs DISABLE TRIGGER validate_spec_on_model_catalogs")
	require.NoError(t, err)

	_, err = db.Exec(`INSERT INTO api.model_catalogs (api_version, kind, metadata, spec)
        VALUES ('v1', 'ModelCatalog', ROW('mc-legacy-empty', NULL, 'default', NULL, NOW(), NOW(), NULL, NULL)::api.metadata, NULL)`)
	require.NoError(t, err)

	_, err = db.Exec("ALTER TABLE api.model_catalogs ENABLE TRIGGER validate_spec_on_model_catalogs")
	require.NoError(t, err)

	var id int
	require.NoError(t, db.QueryRow(
		"SELECT id FROM api.model_catalogs WHERE (metadata).name = 'mc-legacy-empty'",
	).Scan(&id))

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM api.model_catalogs WHERE id = $1", id)
	})

	// The controller can still move it through its state machine ...
	require.NoError(t, s.UpdateModelCatalog(strconv.Itoa(id), &v1.ModelCatalog{
		Status: &v1.ModelCatalogStatus{Phase: v1.ModelCatalogPhaseFAILED},
	}))

	// ... and an operator can still repair it.
	require.NoError(t, s.UpdateModelCatalog(strconv.Itoa(id), &v1.ModelCatalog{
		Spec: &v1.ModelCatalogSpec{Model: &v1.ModelSpec{Name: "org/repaired"}},
	}))

	assert.Equal(t, "org/repaired", modelNameOf(t, s, id))
}
