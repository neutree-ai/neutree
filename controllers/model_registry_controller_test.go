package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	modelregistrymocks "github.com/neutree-ai/neutree/pkg/model_registry/mocks"
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
			name:  "Pending/NoStatus -> Connected (connect success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Connect").Return(nil)
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
				m.On("Connect").Return(assert.AnError)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
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
			name:  "Pending/NoStatus -> Pending/NoStatus (disconnect failed)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
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
			name:  "Connected -> Connected (healthy check success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("HealthyCheck").Return(true)
			},
		},
		{
			name:  "Connected -> Failed (healthy check failed)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("HealthyCheck").Return(false)
				s.On("UpdateModelRegistry", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.ModelRegistry)
					assert.Equal(t, v1.ModelRegistryPhaseFAILED, obj.Status.Phase)
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
			name:  "Connected -> Connected (disconnect failed)",
			input: testModelRegistryWithDeletionTimestamp(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
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
			name:  "Failed -> Connected (reconnect success)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(nil)
				m.On("Connect").Return(nil)
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
			},
			wantErr: true,
		},
		{
			name:  "Failed -> Failed (disconnect error)",
			input: testModelRegistry(),
			mockSetup: func(input *v1.ModelRegistry, s *storagemocks.MockStorage, m *modelregistrymocks.MockModelRegistry) {
				m.On("Disconnect").Return(assert.AnError)
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
