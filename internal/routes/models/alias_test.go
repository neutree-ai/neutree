package models

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	model_registry_mocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// aliasTable is an in-memory stand-in for api.model_aliases, unique index and
// all. The uniqueness rule is the database's to enforce, so a test of the
// handler's behaviour under contention is only meaningful against something that
// actually enforces it.
type aliasTable struct {
	mu     sync.Mutex
	nextID int
	rows   []v1.ModelAlias
}

func (a *aliasTable) list(_ storage.ListOption) []v1.ModelAlias {
	a.mu.Lock()
	defer a.mu.Unlock()

	return append([]v1.ModelAlias(nil), a.rows...)
}

func (a *aliasTable) create(row *v1.ModelAlias) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	for _, existing := range a.rows {
		if existing.ModelRegistryID == row.ModelRegistryID && existing.AliasNormalized == row.AliasNormalized {
			return errors.New(`{"code":"23505","message":"duplicate key value violates unique constraint"}`)
		}
	}

	a.nextID++
	stored := *row
	stored.ID = a.nextID
	a.rows = append(a.rows, stored)

	return nil
}

func (a *aliasTable) update(id string, row *v1.ModelAlias) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	for i := range a.rows {
		if a.rows[i].ModelRegistryID == row.ModelRegistryID &&
			a.rows[i].AliasNormalized == row.AliasNormalized &&
			idOf(a.rows[i]) != id {
			return errors.New(`{"code":"23505","message":"duplicate key value violates unique constraint"}`)
		}
	}

	for i := range a.rows {
		if idOf(a.rows[i]) == id {
			stored := *row
			stored.ID = a.rows[i].ID
			a.rows[i] = stored

			return nil
		}
	}

	return nil
}

func (a *aliasTable) delete(id string) error {
	a.mu.Lock()
	defer a.mu.Unlock()

	remaining := a.rows[:0]

	for _, row := range a.rows {
		if idOf(row) != id {
			remaining = append(remaining, row)
		}
	}

	a.rows = remaining

	return nil
}

func idOf(row v1.ModelAlias) string {
	return strconv.Itoa(row.ID)
}

// bindAliasTable wires the in-memory table into a mock storage.
func bindAliasTable(t *testing.T, mockStorage *mocks.MockStorage) *aliasTable {
	t.Helper()

	table := &aliasTable{}

	// Which of these a given test exercises depends on what the handler decides
	// to do, so none of them is required to be called.
	mockStorage.On("ListModelAlias", mock.Anything).
		Return(table.list, func(storage.ListOption) error { return nil }).Maybe()
	mockStorage.On("CreateModelAlias", mock.Anything).Return(table.create).Maybe()
	mockStorage.On("UpdateModelAlias", mock.Anything, mock.Anything).Return(table.update).Maybe()
	mockStorage.On("DeleteModelAlias", mock.Anything).Return(table.delete).Maybe()

	return table
}

func newPatchContext(t *testing.T, workspace, registryName, modelName, version, body string) (
	*gin.Context, *httptest.ResponseRecorder) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	url := "/api/v1/workspaces/" + workspace + "/model_registries/" + registryName + "/models/" + modelName
	if version != "" {
		url += "?version=" + version
	}

	c.Request = httptest.NewRequest(http.MethodPatch, url, strings.NewReader(body))
	c.Request.Header.Set("Content-Type", "application/json")
	c.Params = []gin.Param{
		{Key: "workspace", Value: workspace},
		{Key: "registry", Value: registryName},
		{Key: "model", Value: modelName},
	}

	return c, w
}

// aliasTestRegistry stands up a private registry holding the given models, and
// returns dependencies wired to an alias table that enforces uniqueness.
func aliasTestRegistry(t *testing.T, models ...v1.GeneralModel) (
	*Dependencies, *mocks.MockStorage, *model_registry_mocks.MockModelRegistry, *aliasTable) {
	t.Helper()

	mockStorage, mockRegistry := setupMocks(t)
	table := bindAliasTable(t, mockStorage)

	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       7,
		Metadata: &v1.Metadata{Name: "test-registry", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.BentoMLModelRegistryType},
	}}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("ListModels", mock.Anything).
		Return(&model_registry.ModelPage{Models: models, Total: len(models)}, nil).Maybe()

	return &Dependencies{Storage: mockStorage}, mockStorage, mockRegistry, table
}

func storedModel(name string, versions ...string) v1.GeneralModel {
	model := v1.GeneralModel{Name: name}
	for _, version := range versions {
		model.Versions = append(model.Versions, v1.ModelVersion{Name: version})
	}

	return model
}

func TestPatchModel_SetsAlias(t *testing.T) {
	deps, mockStorage, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Qwen3 Chat"}`)
	patchModel(deps)(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	rows := table.list(storage.ListOption{})
	require.Len(t, rows, 1)
	// The verbatim spelling is the display name; the normalized form is only for
	// comparison.
	assert.Equal(t, "Qwen3 Chat", rows[0].Alias)
	assert.Equal(t, "qwen3 chat", rows[0].AliasNormalized)
	assert.Equal(t, 7, rows[0].ModelRegistryID)
	assert.Equal(t, "qwen3", rows[0].ModelName)
	assert.Equal(t, "v1", rows[0].ModelVersion)

	var response v1.ModelVersion
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &response))
	assert.Equal(t, "Qwen3 Chat", response.Alias)

	// An alias is a label in the database and nothing else. The registry is not
	// written to at all, so the physical name, version and stored path a running
	// deployment resolves against cannot have moved.
	mockRegistry.AssertNotCalled(t, "SetManualModelInfo", mock.Anything, mock.Anything, mock.Anything)
	mockRegistry.AssertNotCalled(t, "ImportModel", mock.Anything, mock.Anything, mock.Anything, mock.Anything)
	mockRegistry.AssertNotCalled(t, "DeleteModel", mock.Anything, mock.Anything)
	mockRegistry.AssertNotCalled(t, "GetModelPath", mock.Anything, mock.Anything)

	mockStorage.AssertExpectations(t)
}

// Uniqueness is per registry and spans versions: the alias is what a user picks
// a model by, so two versions offering the same one is exactly the ambiguity the
// rule exists to prevent.
func TestPatchModel_AliasConflictsAcrossVersions(t *testing.T) {
	deps, _, mockRegistry, _ := aliasTestRegistry(t, storedModel("qwen3", "v1", "v2"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)
	mockRegistry.On("GetModelDetail", "qwen3", "v2").Return(&v1.ModelVersion{Name: "v2"}, nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Chat"}`)
	patchModel(deps)(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	// Same alias, different version, and normalization must catch the variants.
	for _, alias := range []string{"Chat", "chat", "  CHAT  "} {
		c, w = newPatchContext(t, "default", "test-registry", "qwen3", "v2", `{"alias":"`+alias+`"}`)
		patchModel(deps)(c)

		require.Equal(t, http.StatusConflict, w.Code, "alias %q should have collided", alias)

		var body aliasConflictError
		require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
		assert.Equal(t, aliasConflictKindModel, body.Conflict.Kind)
		assert.Equal(t, "qwen3", body.Conflict.Name)
		assert.Equal(t, "v1", body.Conflict.Version)
	}
}

func TestPatchModel_SameAliasInAnotherRegistryIsAllowed(t *testing.T) {
	deps, mockStorage, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	// A row belonging to a different registry holds the same normalized alias.
	require.NoError(t, table.create(&v1.ModelAlias{
		ModelRegistryID: 99,
		ModelName:       "other",
		ModelVersion:    "v1",
		Alias:           "Chat",
		AliasNormalized: "chat",
	}))

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Chat"}`)
	patchModel(deps)(c)

	assert.Equal(t, http.StatusOK, w.Code, w.Body.String())
	mockStorage.AssertExpectations(t)
}

// An alias that shadows a physical model name would make the model selector
// offer the same name twice.
func TestPatchModel_AliasCannotShadowAModelName(t *testing.T) {
	deps, _, mockRegistry, _ := aliasTestRegistry(t,
		storedModel("qwen3", "v1"), storedModel("llama", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Llama"}`)
	patchModel(deps)(c)

	require.Equal(t, http.StatusConflict, w.Code)

	var body aliasConflictError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, aliasConflictKindModelName, body.Conflict.Kind)
	assert.Equal(t, "llama", body.Conflict.Name)
}

// The alias table is a projection of the registry filesystem. A row whose model
// has since been removed out of band reserves nothing.
func TestPatchModel_OrphanedAliasDoesNotReserveTheName(t *testing.T) {
	deps, _, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	require.NoError(t, table.create(&v1.ModelAlias{
		ModelRegistryID: 7,
		ModelName:       "deleted-behind-our-back",
		ModelVersion:    "v9",
		Alias:           "Chat",
		AliasNormalized: "chat",
	}))

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Chat"}`)
	patchModel(deps)(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	rows := table.list(storage.ListOption{})
	require.Len(t, rows, 1)
	assert.Equal(t, "qwen3", rows[0].ModelName)
}

func TestPatchModel_ClearsAlias(t *testing.T) {
	deps, _, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Chat"}`)
	patchModel(deps)(c)
	require.Equal(t, http.StatusOK, w.Code)

	c, w = newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":""}`)
	patchModel(deps)(c)
	require.Equal(t, http.StatusOK, w.Code, w.Body.String())

	assert.Empty(t, table.list(storage.ListOption{}))
}

func TestPatchModel_RejectsUnusableAlias(t *testing.T) {
	deps, _, mockRegistry, _ := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1",
		`{"alias":"`+strings.Repeat("x", 129)+`"}`)
	patchModel(deps)(c)

	assert.Equal(t, http.StatusBadRequest, w.Code)
}

// Two writers racing for the same alias: the checks upstream both pass against
// their own snapshot, and the unique index is what settles it. Exactly one must
// win and the loser must be told it is a conflict, not a server error.
func TestPatchModel_ConcurrentAliasWritesLeaveExactlyOneWinner(t *testing.T) {
	deps, _, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1", "v2"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)
	mockRegistry.On("GetModelDetail", "qwen3", "v2").Return(&v1.ModelVersion{Name: "v2"}, nil)

	var (
		wg      sync.WaitGroup
		mu      sync.Mutex
		results []int
	)

	// Both requests are built up front: gin.SetMode writes package state, which is
	// the test harness racing with itself rather than anything under test.
	requests := make([]*gin.Context, 0, 2)
	recorders := make([]*httptest.ResponseRecorder, 0, 2)

	for _, version := range []string{"v1", "v2"} {
		c, w := newPatchContext(t, "default", "test-registry", "qwen3", version, `{"alias":"Chat"}`)
		requests = append(requests, c)
		recorders = append(recorders, w)
	}

	// A gate so both requests start together and can interleave.
	start := make(chan struct{})

	for i := range requests {
		wg.Add(1)

		go func(c *gin.Context, w *httptest.ResponseRecorder) {
			defer wg.Done()

			<-start
			patchModel(deps)(c)

			mu.Lock()
			results = append(results, w.Code)
			mu.Unlock()
		}(requests[i], recorders[i])
	}

	close(start)
	wg.Wait()

	assert.ElementsMatch(t, []int{http.StatusOK, http.StatusConflict}, results)

	rows := table.list(storage.ListOption{})
	assert.Len(t, rows, 1)
	assert.Equal(t, "chat", rows[0].AliasNormalized)
}

// The loser of a race is told which model took the alias, not merely that
// something did.
func TestPatchModel_LostRaceNamesTheHolder(t *testing.T) {
	deps, mockStorage, mockRegistry, table := aliasTestRegistry(t, storedModel("qwen3", "v1", "v2"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	// Slip a row in between the pre-checks and the write, the way a concurrent
	// request would.
	mockStorage.ExpectedCalls = filterOutCall(mockStorage.ExpectedCalls, "CreateModelAlias")
	mockStorage.On("CreateModelAlias", mock.Anything).Return(func(row *v1.ModelAlias) error {
		_ = table.create(&v1.ModelAlias{
			ModelRegistryID: row.ModelRegistryID,
			ModelName:       "qwen3",
			ModelVersion:    "v2",
			Alias:           row.Alias,
			AliasNormalized: row.AliasNormalized,
		})

		return table.create(row)
	})

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1", `{"alias":"Chat"}`)
	patchModel(deps)(c)

	require.Equal(t, http.StatusConflict, w.Code)

	var body aliasConflictError
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &body))
	assert.Equal(t, aliasConflictKindModel, body.Conflict.Kind)
	assert.Equal(t, "qwen3", body.Conflict.Name)
	assert.Equal(t, "v2", body.Conflict.Version)
}

func filterOutCall(calls []*mock.Call, method string) []*mock.Call {
	remaining := calls[:0]

	for _, call := range calls {
		if call.Method != method {
			remaining = append(remaining, call)
		}
	}

	return remaining
}

// A public registry has nothing to write to, and says so plainly rather than
// failing somewhere deeper.
func TestPatchModel_UnsupportedOnPublicRegistry(t *testing.T) {
	mockStorage, mockRegistry := setupMocks(t)
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{{
		ID:       8,
		Metadata: &v1.Metadata{Name: "hf", Workspace: "default"},
		Spec:     &v1.ModelRegistrySpec{Type: v1.HuggingFaceModelRegistryType},
	}}, nil)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("Disconnect").Return(nil)
	mockRegistry.On("GetModelDetail", "qwen3", v1.LatestVersion).
		Return(nil, errors.Wrap(model_registry.ErrNotSupported, "hugging face"))

	c, w := newPatchContext(t, "default", "hf", "qwen3", "", `{"alias":"Chat"}`)
	patchModel(&Dependencies{Storage: mockStorage})(c)

	require.Equal(t, http.StatusBadRequest, w.Code)
	assert.Contains(t, w.Body.String(), "does not support")
}

// Hand-filled values are recorded as the user's, whatever the request claimed
// about their provenance.
func TestPatchModel_RecordsManualModelInfo(t *testing.T) {
	deps, _, mockRegistry, _ := aliasTestRegistry(t, storedModel("qwen3", "v1"))
	mockRegistry.On("GetModelDetail", "qwen3", "v1").Return(&v1.ModelVersion{Name: "v1"}, nil)

	var recorded *v1.ModelInfo

	mockRegistry.On("SetManualModelInfo", "qwen3", "v1", mock.Anything).Run(func(args mock.Arguments) {
		recorded = args.Get(2).(*v1.ModelInfo) //nolint:errcheck
	}).Return(nil)

	c, w := newPatchContext(t, "default", "test-registry", "qwen3", "v1",
		`{"info":{"quantization":"fp8","num_hidden_layers":36,"field_sources":{"quantization":"auto"}}}`)
	patchModel(deps)(c)

	require.Equal(t, http.StatusOK, w.Code, w.Body.String())
	require.NotNil(t, recorded)
	assert.Equal(t, "fp8", recorded.Quantization)
	require.NotNil(t, recorded.NumHiddenLayers)
	assert.Equal(t, 36, *recorded.NumHiddenLayers)
	assert.Equal(t, v1.ModelInfoSourceManual, recorded.FieldSources[v1.ModelInfoFieldQuantization])
	assert.Equal(t, v1.ModelInfoSourceManual, recorded.FieldSources[v1.ModelInfoFieldNumHiddenLayers])
}

func TestIsUniqueViolation(t *testing.T) {
	assert.True(t, isUniqueViolation(errors.New(`{"code":"23505","message":"duplicate key"}`)))
	assert.False(t, isUniqueViolation(errors.New(`{"code":"42501","message":"permission denied"}`)))
	assert.False(t, isUniqueViolation(nil))
}
