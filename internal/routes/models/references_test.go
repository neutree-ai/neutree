package models

import (
	"encoding/json"
	"net/http"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func catalogWithModel(name string, model *v1.ModelSpec) v1.ModelCatalog {
	return v1.ModelCatalog{
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec:     &v1.ModelCatalogSpec{Model: model},
	}
}

func recipeCatalog(name string, variants map[string]v1.RecipeVariant) v1.ModelCatalog {
	return v1.ModelCatalog{
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec:     &v1.ModelCatalogSpec{Variants: variants},
	}
}

// deletionTestDeps wires a deletion check against fixed endpoint and catalog
// listings.
func deletionTestDeps(t *testing.T, endpoints []v1.Endpoint, catalogs []v1.ModelCatalog) *Dependencies {
	t.Helper()

	mockStorage, mockRegistry := setupMocks(t)

	mockStorage.On("ListEndpoint", endpointModelReferenceFilterMatcher("default", "test-registry", "test-model")).
		Return(endpoints, nil)
	mockStorage.On("ListModelCatalog", catalogReferenceFilterMatcher("default")).Return(catalogs, nil)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil).Maybe()
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{}, nil).Maybe()
	mockStorage.On("DeleteModelAlias", mock.Anything).Return(nil).Maybe()
	mockRegistry.On("Connect").Return(nil).Maybe()
	mockRegistry.On("Disconnect").Return(nil).Maybe()
	mockRegistry.On("GetModelVersion", "test-model", mock.Anything).
		Return(&v1.ModelVersion{Name: "v1.0.0"}, nil).Maybe()
	mockRegistry.On("DeleteModel", "test-model", mock.Anything).Return(nil).Maybe()

	return &Dependencies{Storage: mockStorage}
}

// decodeReferences pulls the references array out of a 10131 body.
func decodeReferences(t *testing.T, body []byte) []ModelReference {
	t.Helper()

	var response struct {
		Code       string           `json:"code"`
		Hint       string           `json:"hint"`
		References []ModelReference `json:"references"`
	}

	require.NoError(t, json.Unmarshal(body, &response))
	assert.Equal(t, "10131", response.Code)
	assert.Contains(t, response.Hint, "still reference this model")

	return response.References
}

// An endpoint marked for deletion is on its way out. Counting it left the user
// with a model they could not delete and no way to break the deadlock: the
// endpoint was already gone as far as they could see.
func TestDeleteModel_SoftDeletedEndpointDoesNotBlock(t *testing.T) {
	softDeleted := endpointWithModel("test-registry", "test-model", "v1.0.0")
	softDeleted.Metadata = &v1.Metadata{
		Name:              "going-away",
		Workspace:         "default",
		DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
	}

	// The query filters soft-deleted endpoints out at the database, so a correct
	// implementation never sees this row. The listing returns nothing precisely
	// because the filter the matcher asserts on is in the query.
	deps := deletionTestDeps(t, []v1.Endpoint{}, []v1.ModelCatalog{})

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
	assert.Empty(t, w.Body.String())
}

func TestDeleteModel_BlockedByPlainCatalog(t *testing.T) {
	deps := deletionTestDeps(t, []v1.Endpoint{}, []v1.ModelCatalog{
		catalogWithModel("qwen-card", &v1.ModelSpec{
			Registry: "test-registry",
			Name:     "test-model",
			Version:  "v1.0.0",
		}),
	})

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	require.Equal(t, http.StatusBadRequest, w.Code)

	references := decodeReferences(t, w.Body.Bytes())
	require.Len(t, references, 1)
	assert.Equal(t, modelCatalogKind, references[0].Kind)
	assert.Equal(t, "qwen-card", references[0].Name)
	assert.Equal(t, "default", references[0].Workspace)
	assert.Empty(t, references[0].Variant)
}

// A recipe catalog keeps its variants under arbitrary JSON keys, so the check
// has to walk them — and name the one that matched, or the user has no idea
// which entry to edit.
func TestDeleteModel_BlockedByRecipeVariant(t *testing.T) {
	deps := deletionTestDeps(t, []v1.Endpoint{}, []v1.ModelCatalog{
		recipeCatalog("qwen-recipe", map[string]v1.RecipeVariant{
			"fp8": {Model: &v1.ModelSpec{Registry: "test-registry", Name: "other-model"}},
			"bf16-high-throughput": {
				Model: &v1.ModelSpec{Registry: "test-registry", Name: "test-model", Version: "v1.0.0"},
			},
		}),
	})

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	require.Equal(t, http.StatusBadRequest, w.Code)

	references := decodeReferences(t, w.Body.Bytes())
	require.Len(t, references, 1)
	assert.Equal(t, modelCatalogKind, references[0].Kind)
	assert.Equal(t, "qwen-recipe", references[0].Name)
	assert.Equal(t, "bf16-high-throughput", references[0].Variant)
}

// Catalog validation does not require a variant to name a registry, so a variant
// that omits it still points at the model as far as anyone deploying it is
// concerned.
func TestDeleteModel_BlockedByVariantWithoutRegistry(t *testing.T) {
	deps := deletionTestDeps(t, []v1.Endpoint{}, []v1.ModelCatalog{
		recipeCatalog("registry-less", map[string]v1.RecipeVariant{
			"default": {Model: &v1.ModelSpec{Name: "test-model"}},
		}),
	})

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	require.Equal(t, http.StatusBadRequest, w.Code)

	references := decodeReferences(t, w.Body.Bytes())
	require.Len(t, references, 1)
	assert.Equal(t, "default", references[0].Variant)
}

func TestDeleteModel_UnrelatedCatalogsDoNotBlock(t *testing.T) {
	deps := deletionTestDeps(t, []v1.Endpoint{}, []v1.ModelCatalog{
		catalogWithModel("other-model-card", &v1.ModelSpec{
			Registry: "test-registry",
			Name:     "some-other-model",
			Version:  "v1.0.0",
		}),
		catalogWithModel("other-registry-card", &v1.ModelSpec{
			Registry: "another-registry",
			Name:     "test-model",
			Version:  "v1.0.0",
		}),
		catalogWithModel("other-version-card", &v1.ModelSpec{
			Registry: "test-registry",
			Name:     "test-model",
			Version:  "v2.0.0",
		}),
		recipeCatalog("empty-recipe", map[string]v1.RecipeVariant{"a": {}}),
		{Metadata: &v1.Metadata{Name: "spec-less", Workspace: "default"}},
	})

	c, _ := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
}

// The body has to carry enough for a user to act on: what kind of object, where
// it lives, and — for an endpoint still coming up — which stage it is at.
func TestDeleteModel_ReferenceBodyIdentifiesEveryBlocker(t *testing.T) {
	deploying := endpointWithModel("test-registry", "test-model", "v1.0.0")
	deploying.Metadata = &v1.Metadata{Name: "coming-up", Workspace: "default"}
	deploying.Status = &v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING}

	running := endpointWithModel("test-registry", "test-model", v1.LatestVersion)
	running.Metadata = &v1.Metadata{Name: "serving", Workspace: "default"}
	running.Status = &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}

	deps := deletionTestDeps(t, []v1.Endpoint{deploying, running}, []v1.ModelCatalog{
		catalogWithModel("qwen-card", &v1.ModelSpec{Registry: "test-registry", Name: "test-model"}),
	})

	c, w := createMockContext("default", "test-registry", "test-model", "")
	setVersionQuery(c, "v1.0.0")
	deleteModel(deps)(c)

	require.Equal(t, http.StatusBadRequest, w.Code)

	references := decodeReferences(t, w.Body.Bytes())
	require.Len(t, references, 3)

	assert.Contains(t, references, ModelReference{
		Kind:      endpointKind,
		Name:      "coming-up",
		Workspace: "default",
		Phase:     string(v1.EndpointPhaseDEPLOYING),
	})
	assert.Contains(t, references, ModelReference{
		Kind:      endpointKind,
		Name:      "serving",
		Workspace: "default",
		Phase:     string(v1.EndpointPhaseRUNNING),
	})
	assert.Contains(t, references, ModelReference{
		Kind:      modelCatalogKind,
		Name:      "qwen-card",
		Workspace: "default",
	})

	var response map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Contains(t, response["hint"], "2 endpoint(s) and 1 model catalog(s) still reference this model")
}

// The endpoint-only wording predates catalog checking and is asserted elsewhere,
// including by an e2e test, so it has to come out unchanged.
func TestReferenceHint(t *testing.T) {
	endpoint := ModelReference{Kind: endpointKind, Name: "a"}
	catalog := ModelReference{Kind: modelCatalogKind, Name: "b"}

	assert.Equal(t, "1 endpoint(s) still reference this model",
		referenceHint([]ModelReference{endpoint}))
	assert.Equal(t, "2 model catalog(s) still reference this model",
		referenceHint([]ModelReference{catalog, catalog}))
	assert.Equal(t, "1 endpoint(s) and 1 model catalog(s) still reference this model",
		referenceHint([]ModelReference{endpoint, catalog}))
}

func TestVersionsOverlap(t *testing.T) {
	tests := []struct {
		referenced string
		requested  string
		want       bool
	}{
		{referenced: "v1", requested: "v1", want: true},
		{referenced: "v1", requested: "v2", want: false},
		{referenced: "", requested: "v1", want: true},
		{referenced: v1.LatestVersion, requested: "v1", want: true},
		{referenced: "v1", requested: v1.LatestVersion, want: true},
		{referenced: "v1", requested: "", want: true},
	}

	for _, tt := range tests {
		assert.Equal(t, tt.want, versionsOverlap(tt.referenced, tt.requested),
			"referenced=%q requested=%q", tt.referenced, tt.requested)
	}
}

// The deletion handler drops the alias rows of the model it just removed, so a
// later model reusing those coordinates does not inherit a stale display name.
func TestDeleteModel_RemovesAliasRows(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)
	mockStorage.On("ListModelCatalog", mock.Anything).Return([]v1.ModelCatalog{}, nil)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).Return([]v1.ModelAlias{{
		ID:              3,
		ModelRegistryID: 7,
		ModelName:       "test-model",
		ModelVersion:    "v1.0.0",
		Alias:           "Chat",
	}}, nil)
	mockStorage.On("DeleteModelAlias", "3").Return(nil)

	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	// The version has to be resolved before the model goes away; afterwards
	// "latest" points at nothing.
	mockRegistry.On("GetModelVersion", "test-model", v1.LatestVersion).
		Return(&v1.ModelVersion{Name: "v1.0.0"}, nil)
	mockRegistry.On("DeleteModel", "test-model", v1.LatestVersion).Return(nil)

	c, _ := createMockContext("default", "test-registry", "test-model", "")
	deleteModel(&Dependencies{Storage: mockStorage})(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
	mockStorage.AssertExpectations(t)
	mockRegistry.AssertExpectations(t)
}

// A failure to clean up an alias must not fail the deletion: the model is gone,
// and a row pointing at a model that no longer exists is invisible to every read
// path anyway.
func TestDeleteModel_AliasCleanupIsBestEffort(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)

	mockStorage.On("ListEndpoint", mock.Anything).Return([]v1.Endpoint{}, nil)
	mockStorage.On("ListModelCatalog", mock.Anything).Return([]v1.ModelCatalog{}, nil)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockStorage.On("ListModelAlias", mock.Anything).
		Return([]v1.ModelAlias(nil), assert.AnError)

	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("GetModelVersion", "test-model", v1.LatestVersion).
		Return(&v1.ModelVersion{Name: "v1.0.0"}, nil)
	mockRegistry.On("DeleteModel", "test-model", v1.LatestVersion).Return(nil)

	c, _ := createMockContext("default", "test-registry", "test-model", "")
	deleteModel(&Dependencies{Storage: mockStorage})(c)

	assert.Equal(t, http.StatusNoContent, c.Writer.Status())
}

// The endpoint query must ask the database for the soft-delete filter rather
// than sorting it out in Go, so the filter set itself is the assertion.
func TestEndpointReferences_QueryFiltersSoftDeleted(t *testing.T) {
	mockStorage, _ := setupMocks(t)

	var captured storage.ListOption

	mockStorage.On("ListEndpoint", mock.Anything).Run(func(args mock.Arguments) {
		captured = args.Get(0).(storage.ListOption) //nolint:errcheck
	}).Return([]v1.Endpoint{}, nil)

	_, err := endpointReferences(&Dependencies{Storage: mockStorage},
		"default", "test-registry", "test-model", "v1.0.0")
	require.NoError(t, err)

	assert.Contains(t, captured.Filters, storage.Filter{
		Column:   "metadata->>deletion_timestamp",
		Operator: "is",
		Value:    "null",
	})
}
