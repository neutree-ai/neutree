package dbtest

import (
	"database/sql"
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// createTestModelRegistry inserts a model registry in the default workspace
// through PostgREST and registers its removal. It returns the row id.
func createTestModelRegistry(t *testing.T, db *sql.DB, s storage.Storage, name string) int {
	t.Helper()

	require.NoError(t, s.CreateModelRegistry(&v1.ModelRegistry{
		APIVersion: "v1",
		Kind:       "ModelRegistry",
		Metadata:   &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.BentoMLModelRegistryType,
			Url:  "file://localhost/tmp/" + name,
		},
		Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
	}))

	var id int
	require.NoError(t, db.QueryRow(
		"SELECT id FROM api.model_registries WHERE (metadata).name = $1 AND (metadata).workspace = 'default'",
		name,
	).Scan(&id))

	t.Cleanup(func() {
		_, _ = db.Exec("DELETE FROM api.model_registries WHERE id = $1", id)
	})

	return id
}

// TestModelRegistryStatusPatchReplacesWholeComposite pins down what PostgREST
// does to a composite-type column when the PATCH body mentions only some of its
// attributes. The answer -- the column is replaced, so unmentioned attributes
// end up NULL -- is the entire reason status writers have to carry attributes
// forward by hand (see ClusterController.updateStatus and
// ModelRegistryController.updateStatus).
//
// If PostgREST ever merges attribute by attribute instead, this test fails and
// that hand-written carry-forward can be deleted.
func TestModelRegistryStatusPatchReplacesWholeComposite(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelRegistry(t, db, s, "patch-semantics")

	// Populate every attribute of the composite, including ones the Go struct
	// used for the PATCH below does not send. The attribute order is the order
	// they were added in: phase, last_transition_time, error_message, then stats
	// (081) and last_checked_at (084).
	_, err := db.Exec(`UPDATE api.model_registries
		SET status = ROW('Connected', '2026-01-01T00:00:00Z', 'previous failure',
		                 '{"model_count": 3, "storage_bytes": 4096}'::jsonb,
		                 '2026-01-01T00:00:00Z')::api.model_registry_status
		WHERE id = $1`, id)
	require.NoError(t, err)

	// A PATCH carrying only phase + last_transition_time.
	require.NoError(t, s.UpdateModelRegistry(strconv.Itoa(id), &v1.ModelRegistry{
		Status: &v1.ModelRegistryStatus{
			Phase:              v1.ModelRegistryPhaseCONNECTED,
			LastTransitionTime: "2026-02-02T00:00:00Z",
		},
	}))

	var (
		phaseSet, timeSet bool
		errorMessageNull  bool
		statsNull         bool
		lastCheckedAtNull bool
	)

	require.NoError(t, db.QueryRow(`SELECT
			(status).phase IS NOT NULL,
			(status).last_transition_time IS NOT NULL,
			(status).error_message IS NULL,
			(status).stats IS NULL,
			(status).last_checked_at IS NULL
		FROM api.model_registries WHERE id = $1`, id).
		Scan(&phaseSet, &timeSet, &errorMessageNull, &statsNull, &lastCheckedAtNull))

	assert.True(t, phaseSet, "attributes named in the PATCH body are written")
	assert.True(t, timeSet, "attributes named in the PATCH body are written")
	assert.True(t, errorMessageNull, "PostgREST replaces the whole composite: error_message should be nulled")
	assert.True(t, statsNull, "PostgREST replaces the whole composite: stats should be nulled")
	assert.True(t, lastCheckedAtNull,
		"PostgREST replaces the whole composite: last_checked_at should be nulled too, which is why "+
			"model_registry.NextStatus has to carry it forward")
}

// TestModelRegistryStatsSurviveConnectivityReconcile is the regression test for
// the status write-back defect bundled with NEU-619: it drives a real
// connectivity reconcile against a real PostgREST and asserts the statistics
// written by the (separate) statistics path are still there afterwards.
//
// Without the carry-forward in ModelRegistryController.updateStatus this fails.
func TestModelRegistryStatsSurviveConnectivityReconcile(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelRegistry(t, db, s, "stats-reconcile")

	_, err := db.Exec(`UPDATE api.model_registries
		SET status = ROW('Connected', '2026-01-01T00:00:00Z', NULL,
		                 '{"model_count": 3, "storage_bytes": 4096, "stats_updated_at": "2026-01-01T00:00:00Z"}'::jsonb,
		                 '2026-01-01T00:00:00Z')::api.model_registry_status
		WHERE id = $1`, id)
	require.NoError(t, err)

	obj, err := s.GetModelRegistry(strconv.Itoa(id))
	require.NoError(t, err)
	require.NotNil(t, obj.Status.Stats, "precondition: stats were stored")

	// Stub out the registry backend so the reconcile is exercised without a real
	// BentoML store: the connectivity check fails, which is what pushes the
	// controller into a status write-back.
	mockRegistry := modelregistrymocks.NewMockModelRegistry(t)
	mockRegistry.On("Connect").Return(nil)
	mockRegistry.On("HealthyCheck").Return(assert.AnError)

	original := model_registry.NewModelRegistry
	model_registry.NewModelRegistry = func(_ *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
		return mockRegistry, nil
	}

	defer func() { model_registry.NewModelRegistry = original }()

	c, err := controllers.NewModelRegistryController(&controllers.ModelRegistryControllerOption{Storage: s})
	require.NoError(t, err)

	require.Error(t, c.Reconcile(obj), "the stubbed health check fails, so the reconcile reports an error")

	after, err := s.GetModelRegistry(strconv.Itoa(id))
	require.NoError(t, err)

	assert.Equal(t, v1.ModelRegistryPhaseFAILED, after.Status.Phase, "the reconcile did write the status back")
	require.NotNil(t, after.Status.Stats, "stats must survive a connectivity status write-back")
	assert.Equal(t, 3, after.Status.Stats.ModelCount)
	assert.Equal(t, int64(4096), after.Status.Stats.StorageBytes)
	assert.Equal(t, "2026-01-01T00:00:00Z", after.Status.Stats.StatsUpdatedAt)

	// The other half of the same write: the reconcile did check the registry, so
	// the check time moves rather than being carried over. This is the only place
	// last_checked_at (084) is exercised against a real PostgREST.
	require.NotEmpty(t, after.Status.LastCheckedAt, "a reconcile records when it checked")
	assert.NotEqual(t, "2026-01-01T00:00:00Z", after.Status.LastCheckedAt,
		"the seeded check time is stale, so this reconcile must have replaced it")
}

// TestModelRegistryStatsReadableThroughPostgREST checks the new attribute is
// visible on the REST surface, not just in the table.
func TestModelRegistryStatsReadableThroughPostgREST(t *testing.T) {
	db := GetTestDB(t)
	s := NewTestStorage(t)

	id := createTestModelRegistry(t, db, s, "stats-readable")

	require.NoError(t, s.UpdateModelRegistry(strconv.Itoa(id), &v1.ModelRegistry{
		Status: &v1.ModelRegistryStatus{
			Phase: v1.ModelRegistryPhaseCONNECTED,
			Stats: &v1.ModelRegistryStats{
				ModelCount:     7,
				StorageBytes:   1 << 30,
				StatsUpdatedAt: "2026-03-03T00:00:00Z",
			},
		},
	}))

	got, err := s.GetModelRegistry(strconv.Itoa(id))
	require.NoError(t, err)
	require.NotNil(t, got.Status.Stats)
	assert.Equal(t, 7, got.Status.Stats.ModelCount)
	assert.Equal(t, int64(1<<30), got.Status.Stats.StorageBytes)
	assert.Equal(t, "2026-03-03T00:00:00Z", got.Status.Stats.StatsUpdatedAt)
}
