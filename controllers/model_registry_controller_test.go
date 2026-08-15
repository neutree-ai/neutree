package controllers

import (
	"errors"
	"testing"
	"time"

	pkgerrors "github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/internal/model_registry/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newTestModelRegistryController(storage *storagemocks.MockStorage, model *modelregistrymocks.MockModelRegistry) *ModelRegistryController {
	model_registry.NewModelRegistry = func(obj *v1.ModelRegistry) (model_registry.ModelRegistry, error) {
		return model, nil
	}

	return &ModelRegistryController{
		storage: storage,
	}
}

func TestModelRegistryController_Sync_Delete(t *testing.T) {
	testModelRegistry := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseDELETED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		mockSetup func(*v1.ModelRegistry, *storagemocks.MockStorage, *modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name:  "Deleted -> Deleted (storage delete success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("DeleteModelRegistry", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Deleted -> Deleted (storage delete failed)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("DeleteModelRegistry", "1").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(tt.input, mockStorage, mockModel)

			c := newTestModelRegistryController(mockStorage, mockModel)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}

func TestModelRegistryController_Sync_PendingOrNoStatus(t *testing.T) {
	testModelRegistry := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
		}
	}

	testModelRegistryWithDeletionTimestamp := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		mockSetup func(*v1.ModelRegistry, *storagemocks.MockStorage, *modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name:  "Pending/NoStatus -> Connected (connect and health check success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
				m.On("Disconnect").Return(nil)
				m.On("HealthyCheck").Return(nil)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, obj.Status.Phase)
				}).Return(nil)
			},
		},
		{
			name:  "Pending/NoStatus -> Failed (connect error)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(errors.New("failed to read NFS mount path /mnt/registry"))
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
					assert.Contains(t, obj.Status.ErrorMessage, "/mnt/registry")
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Pending/NoStatus -> Deleted (disconnect success)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseDELETED, obj.Status.Phase)
				}).Return(nil)
				m.On("Disconnect").Return(nil)
			},
		},
		{
			name:  "Pending/NoStatus -> Failed (disconnect failed)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
				// Now updateStatus is called even on disconnect failure to set FAILED phase
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(tt.input, mockStorage, mockModel)

			c := newTestModelRegistryController(mockStorage, mockModel)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}

func TestModelRegistryController_Sync_Connected(t *testing.T) {
	testModelRegistry := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
		}
	}

	testModelRegistryWithDeletionTimestamp := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		mockSetup func(*v1.ModelRegistry, *storagemocks.MockStorage, *modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name:  "Connected -> Connected (connect and health check success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
				m.On("Disconnect").Return(nil)
				m.On("HealthyCheck").Return(nil)
				// A registry with no counters yet is measured on the spot, and the
				// result is written back even though the phase did not change.
				m.On("CollectUsage").Return(&model_registry.RegistryUsage{ModelCount: 2, StorageBytes: 8192}, nil)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, obj.Status.Phase)
					assert.Equal(t, 2, obj.Status.Stats.ModelCount)
					assert.Equal(t, int64(8192), obj.Status.Stats.StorageBytes)
					assert.NotEmpty(t, obj.Status.Stats.StatsUpdatedAt)
				}).Return(nil)
			},
		},
		{
			name:  "Connected -> Failed (healthy check failed)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
				m.On("Disconnect").Return(nil)
				m.On("HealthyCheck").Return(errors.New("timed out reading NFS mount path /mnt/registry"))
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
					assert.Contains(t, obj.Status.ErrorMessage, "/mnt/registry")
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Connected -> Deleted (disconnect success)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseDELETED, obj.Status.Phase)
				}).Return(nil)
				m.On("Disconnect").Return(nil)
			},
		},
		{
			name:  "Connected -> Failed (disconnect failed)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
				// Now updateStatus is called even on disconnect failure to set FAILED phase
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(tt.input, mockStorage, mockModel)

			c := newTestModelRegistryController(mockStorage, mockModel)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}

func TestModelRegistryController_Sync_Failed(t *testing.T) {
	testModelRegistry := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseFAILED},
		}
	}

	testModelRegistryWithDeletionTimestamp := func() *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Status: &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseFAILED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		mockSetup func(*v1.ModelRegistry, *storagemocks.MockStorage, *modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name:  "Failed -> Connected (disconnect, reconnect, and health check success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(nil)
				m.On("HealthyCheck").Return(nil)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, obj.Status.Phase)
				}).Return(nil)
			},
		},
		{
			name:  "Failed -> Failed (connect error)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(assert.AnError)
				// Defer block updates status to FAILED when connect fails
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Failed (disconnect error)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
				// Defer block updates status to FAILED when disconnect fails
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Deleted (disconnect success)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseDELETED, obj.Status.Phase)
				}).Return(nil)
				m.On("Disconnect").Return(nil)
			},
		},
		{
			name:  "Failed -> Failed (disconnect failed)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
				// Deletion path updates status to FAILED when disconnect fails
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(tt.input, mockStorage, mockModel)

			c := newTestModelRegistryController(mockStorage, mockModel)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}

// Regression test for the status write-back defect bundled with NEU-619.
//
// updateStatus rebuilds the status struct from scratch, and PostgREST replaces a
// composite-type column as a whole rather than merging attribute by attribute.
// Every attribute the reconcile leaves out of the PATCH body is therefore nulled
// in the database. Before the carry-forward, each of the transitions below wiped
// the statistics that the (separate) statistics path had written.
func TestModelRegistryController_UpdateStatus_CarriesStatsForward(t *testing.T) {
	stats := func() *v1.ModelRegistryStats {
		return &v1.ModelRegistryStats{
			ModelCount:     3,
			StorageBytes:   4096,
			StatsUpdatedAt: "2026-01-01T00:00:00Z",
		}
	}

	testModelRegistry := func(phase v1.ModelRegistryPhase) *v1.ModelRegistry {
		return &v1.ModelRegistry{
			ID:       1,
			Metadata: &v1.Metadata{Name: "test"},
			Status:   &v1.ModelRegistryStatus{Phase: phase, Stats: stats()},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelRegistry
		wantPhase v1.ModelRegistryPhase
		mockSetup func(*modelregistrymocks.MockModelRegistry)
		wantErr   bool
	}{
		{
			name:      "Connected -> Failed (health check failed)",
			input:     testModelRegistry(v1.ModelRegistryPhaseCONNECTED),
			wantPhase: v1.ModelRegistryPhaseFAILED,
			mockSetup: func(m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
				m.On("Disconnect").Return(nil)
				m.On("HealthyCheck").Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name:      "Failed -> Connected (reconnect succeeded)",
			input:     testModelRegistry(v1.ModelRegistryPhaseFAILED),
			wantPhase: v1.ModelRegistryPhaseCONNECTED,
			mockSetup: func(m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(nil)
				m.On("HealthyCheck").Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockModel := &modelregistrymocks.MockModelRegistry{}
			tt.mockSetup(mockModel)

			mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
				obj := args.Get(1).(*v1.ModelRegistry)
				assert.Equal(t, tt.wantPhase, obj.Status.Phase)
				assert.Equal(t, stats(), obj.Status.Stats, "status write-back must not drop stats")
			}).Return(nil)

			c := newTestModelRegistryController(mockStorage, mockModel)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}

			mockStorage.AssertExpectations(t)
			mockModel.AssertExpectations(t)
		})
	}
}

func TestModelRegistryController_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name: "reconcile success",
			input: &v1.ModelRegistry{
				Metadata: &v1.Metadata{Name: "test"},
			},
			mockSetup: func(s *storagemocks.MockStorage) {
			},
			wantErr: false,
		},
		{
			name:    "invalid key type",
			input:   "invalid",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			c := &ModelRegistryController{storage: mockStorage, syncHandler: func(obj *v1.ModelRegistry) error { return nil }}
			err := c.Reconcile(tt.input)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

// The reconcile loop runs every ten seconds; walking a model tree cannot. The
// staleness timestamp is what holds it off, so it has to survive the write-back
// as well as be consulted before the walk.
func TestModelRegistryController_Sync_ThrottlesStatsCollection(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockModel := &modelregistrymocks.MockModelRegistry{}

	mockModel.On("Connect").Return(nil)
	mockModel.On("Disconnect").Return(nil)
	mockModel.On("HealthyCheck").Return(nil)
	mockModel.On("CollectUsage").Return(&model_registry.RegistryUsage{ModelCount: 1, StorageBytes: 64}, nil)

	obj := &v1.ModelRegistry{
		ID:       1,
		Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
		Status:   &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
	}

	// Feed the persisted status back in, the way the next reconcile would read it.
	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		written := args.Get(1).(*v1.ModelRegistry) //nolint:errcheck
		obj.Status = written.Status
	}).Return(nil)

	c := newTestModelRegistryController(mockStorage, mockModel)

	for i := 0; i < 5; i++ {
		assert.NoError(t, c.sync(obj))
	}

	mockModel.AssertNumberOfCalls(t, "CollectUsage", 1)
	mockStorage.AssertNumberOfCalls(t, "UpdateModelRegistry", 1)
	assert.Equal(t, 1, obj.Status.Stats.ModelCount)
	assert.Equal(t, int64(64), obj.Status.Stats.StorageBytes)
}

// An unreachable registry must keep the counters it last reported. Zeroing them
// would show a mount failure as an empty registry.
func TestModelRegistryController_Sync_UnreachableRegistryKeepsStats(t *testing.T) {
	previous := &v1.ModelRegistryStats{
		ModelCount:     4,
		StorageBytes:   2048,
		StatsUpdatedAt: "2026-01-01T00:00:00Z",
	}

	mockStorage := &storagemocks.MockStorage{}
	mockModel := &modelregistrymocks.MockModelRegistry{}

	mockModel.On("Connect").Return(nil)
	mockModel.On("Disconnect").Return(nil)
	mockModel.On("HealthyCheck").Return(assert.AnError)
	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		written := args.Get(1).(*v1.ModelRegistry) //nolint:errcheck
		assert.Equal(t, v1.ModelRegistryPhaseFAILED, written.Status.Phase)
		assert.Equal(t, previous, written.Status.Stats)
	}).Return(nil)

	c := newTestModelRegistryController(mockStorage, mockModel)

	err := c.sync(&v1.ModelRegistry{
		ID:       1,
		Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
		Status:   &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED, Stats: previous},
	})
	assert.Error(t, err)

	mockModel.AssertNotCalled(t, "CollectUsage")
	mockStorage.AssertExpectations(t)
}

// A public registry reports ErrNotSupported rather than a failure, and gets no
// stats block at all — its storage is not ours to measure.
func TestModelRegistryController_Sync_PublicRegistryHasNoStats(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockModel := &modelregistrymocks.MockModelRegistry{}

	mockModel.On("Connect").Return(nil)
	mockModel.On("Disconnect").Return(nil)
	mockModel.On("HealthyCheck").Return(nil)
	mockModel.On("CollectUsage").Return(nil, pkgerrors.Wrap(model_registry.ErrNotSupported, "hugging face"))

	c := newTestModelRegistryController(mockStorage, mockModel)

	obj := &v1.ModelRegistry{
		ID:       1,
		Metadata: &v1.Metadata{Name: "public", Workspace: "default"},
		Status:   &v1.ModelRegistryStatus{Phase: v1.ModelRegistryPhaseCONNECTED},
	}
	var written *v1.ModelRegistryStatus

	// The phase did not change and nothing was measured, but the check still
	// happened, and recording when a registry was last checked is the one thing
	// that has to be written even when the answer is the same as last time.
	mockStorage.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
		obj := args.Get(1).(*v1.ModelRegistry) //nolint:errcheck
		written = obj.Status
	}).Return(nil)

	assert.NoError(t, c.sync(obj))

	assert.Nil(t, obj.Status.Stats)

	if assert.NotNil(t, written) {
		assert.Equal(t, v1.ModelRegistryPhaseCONNECTED, written.Phase)
		assert.NotEmpty(t, written.LastCheckedAt)
		// Still no counters: a public registry's storage is not ours to measure.
		assert.Nil(t, written.Stats)
	}
}
