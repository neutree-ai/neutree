package controllers

import (
	"context"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestNewModelCatalogController(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}

	controller, err := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	assert.NoError(t, err)
	assert.NotNil(t, controller)
	assert.Equal(t, mockStorage, controller.storage)
	assert.Equal(t, 1, controller.baseController.workers)
}

func TestModelCatalogController_ListKeys(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	expectedModelCatalogs := []v1.ModelCatalog{
		{ID: 1, Metadata: &v1.Metadata{Name: "catalog1"}},
		{ID: 2, Metadata: &v1.Metadata{Name: "catalog2"}},
	}

	mockStorage.On("ListModelCatalog", storage.ListOption{}).Return(expectedModelCatalogs, nil)

	keys, err := controller.ListKeys()

	assert.NoError(t, err)
	assert.Len(t, keys, 2)
	assert.Equal(t, 1, keys[0])
	assert.Equal(t, 2, keys[1])

	mockStorage.AssertExpectations(t)
}

func TestModelCatalogController_Reconcile(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	modelCatalog := &v1.ModelCatalog{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:      "test-catalog",
			Workspace: "default",
		},
		Spec: &v1.ModelCatalogSpec{
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
			Engine: &v1.EndpointEngineSpec{
				Engine: "vllm",
			},
		},
		Status: &v1.ModelCatalogStatus{
			Phase: v1.ModelCatalogPhasePENDING,
		},
	}

	mockStorage.On("GetModelCatalog", "1").Return(modelCatalog, nil)
	mockStorage.On("UpdateModelCatalog", "1", mock.AnythingOfType("*v1.ModelCatalog")).Return(nil)

	err := controller.Reconcile(1)

	assert.NoError(t, err)
	mockStorage.AssertExpectations(t)
}

func TestModelCatalogController_processPendingModelCatalog(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	modelCatalog := &v1.ModelCatalog{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:      "test-catalog",
			Workspace: "default",
		},
		Spec: &v1.ModelCatalogSpec{
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
			Engine: &v1.EndpointEngineSpec{
				Engine: "vllm",
			},
		},
		Status: &v1.ModelCatalogStatus{
			Phase: v1.ModelCatalogPhasePENDING,
		},
	}

	mockStorage.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
		return mc.Status.Phase == v1.ModelCatalogPhaseREADY &&
			mc.Spec.Replicas != nil &&
			*mc.Spec.Replicas.Num == 1 &&
			mc.Spec.DeploymentOptions != nil &&
			mc.Spec.Variables != nil
	})).Return(nil)

	err := controller.processPendingModelCatalog(modelCatalog)

	assert.NoError(t, err)
	assert.Equal(t, v1.ModelCatalogPhaseREADY, modelCatalog.Status.Phase)
	assert.NotEmpty(t, modelCatalog.Status.LastTransitionTime)
	assert.Empty(t, modelCatalog.Status.ErrorMessage)
	assert.NotNil(t, modelCatalog.Spec.Resources)
	assert.NotNil(t, modelCatalog.Spec.Replicas)
	assert.Equal(t, 1, *modelCatalog.Spec.Replicas.Num)
	assert.NotNil(t, modelCatalog.Spec.DeploymentOptions)
	assert.NotNil(t, modelCatalog.Spec.Variables)

	mockStorage.AssertExpectations(t)
}

func TestModelCatalogController_processFailedModelCatalog(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	modelCatalog := &v1.ModelCatalog{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:      "test-catalog",
			Workspace: "default",
		},
		Spec: &v1.ModelCatalogSpec{
			Model: &v1.ModelSpec{
				Name: "test-model",
			},
			Engine: &v1.EndpointEngineSpec{
				Engine: "vllm",
			},
		},
		Status: &v1.ModelCatalogStatus{
			Phase: v1.ModelCatalogPhaseFAILED,
		},
	}

	mockStorage.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
		return mc.Status.Phase == v1.ModelCatalogPhasePENDING
	})).Return(nil)

	err := controller.processFailedModelCatalog(modelCatalog)
	assert.NoError(t, err)
	assert.Equal(t, v1.ModelCatalogPhasePENDING, modelCatalog.Status.Phase)

	mockStorage.AssertExpectations(t)
}

func TestModelCatalogController_Start(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	// Mock the ListModelCatalog to return empty list to avoid endless loop in test
	mockStorage.On("ListModelCatalog", storage.ListOption{}).Return([]v1.ModelCatalog{}, nil).Maybe()

	// Start should not block or panic
	assert.NotPanics(t, func() {
		controller.Start(ctx)
	})
}

func TestModelCatalogController_sync_Delete(t *testing.T) {
	testModelCatalog := func() *v1.ModelCatalog {
		return &v1.ModelCatalog{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test-catalog",
				Workspace:         "default",
				DeletionTimestamp: time.Now().Format(time.RFC3339),
			},
			Status: &v1.ModelCatalogStatus{Phase: v1.ModelCatalogPhaseDELETED},
		}
	}

	tests := []struct {
		name      string
		input     *v1.ModelCatalog
		mockSetup func(*v1.ModelCatalog, *storageMocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleted -> Deleted (storage delete success)",
			input: testModelCatalog(),
			mockSetup: func(input *v1.ModelCatalog, s *storageMocks.MockStorage) {
				s.On("DeleteModelCatalog", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Deleted -> Deleted (storage delete failed)",
			input: testModelCatalog(),
			mockSetup: func(input *v1.ModelCatalog, s *storageMocks.MockStorage) {
				s.On("DeleteModelCatalog", "1").Return(errors.New("storage error"))
			},
			wantErr: true,
		},
		{
			name: "Ready -> Deleting",
			input: &v1.ModelCatalog{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-catalog",
					Workspace:         "default",
					DeletionTimestamp: time.Now().Format(time.RFC3339),
				},
				Status: &v1.ModelCatalogStatus{Phase: v1.ModelCatalogPhaseREADY},
			},
			mockSetup: func(input *v1.ModelCatalog, s *storageMocks.MockStorage) {
				s.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
					return mc.Status.Phase == v1.ModelCatalogPhaseDELETED
				})).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Failed -> Deleted (already failed, should delete from DB)",
			input: &v1.ModelCatalog{
				ID: 1,
				Metadata: &v1.Metadata{
					Name:              "test-catalog",
					Workspace:         "default",
					DeletionTimestamp: time.Now().Format(time.RFC3339),
				},
				Status: &v1.ModelCatalogStatus{Phase: v1.ModelCatalogPhaseFAILED},
			},
			mockSetup: func(input *v1.ModelCatalog, s *storageMocks.MockStorage) {
				s.On("DeleteModelCatalog", "1").Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storageMocks.MockStorage{}
			tt.mockSetup(tt.input, mockStorage)

			controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
				Storage: mockStorage,
				Workers: 1,
			})

			err := controller.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestModelCatalogController_updateStatus(t *testing.T) {
	mockStorage := &storageMocks.MockStorage{}
	controller, _ := NewModelCatalogController(&ModelCatalogControllerOption{
		Storage: mockStorage,
		Workers: 1,
	})

	modelCatalog := &v1.ModelCatalog{
		ID: 1,
		Metadata: &v1.Metadata{
			Name:      "test-catalog",
			Workspace: "default",
		},
	}

	tests := []struct {
		name        string
		phase       v1.ModelCatalogPhase
		inputError  error
		mockSetup   func(*storageMocks.MockStorage)
		wantErr     bool
		wantMessage string
	}{
		{
			name:  "Update to DELETED phase with no error",
			phase: v1.ModelCatalogPhaseDELETED,
			mockSetup: func(s *storageMocks.MockStorage) {
				s.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
					return mc.Status.Phase == v1.ModelCatalogPhaseDELETED &&
						mc.Status.ErrorMessage == "" &&
						mc.Status.LastTransitionTime != ""
				})).Return(nil)
			},
			wantErr: false,
		},
		{
			name:       "Update to FAILED phase with error",
			phase:      v1.ModelCatalogPhaseFAILED,
			inputError: errors.New("test error"),
			mockSetup: func(s *storageMocks.MockStorage) {
				s.On("UpdateModelCatalog", "1", mock.MatchedBy(func(mc *v1.ModelCatalog) bool {
					return mc.Status.Phase == v1.ModelCatalogPhaseFAILED &&
						mc.Status.ErrorMessage == "test error" &&
						mc.Status.LastTransitionTime != ""
				})).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Storage update fails",
			phase: v1.ModelCatalogPhaseDELETED,
			mockSetup: func(s *storageMocks.MockStorage) {
				s.On("UpdateModelCatalog", "1", mock.Anything).Return(errors.New("storage error"))
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storageMocks.MockStorage{}
			tt.mockSetup(mockStorage)

			controller.storage = mockStorage

			err := controller.updateStatus(modelCatalog, tt.phase, tt.inputError)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}
