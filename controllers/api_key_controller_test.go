package controllers

import (
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	gatewaymocks "github.com/neutree-ai/neutree/internal/gateway/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestApiKeyController is a helper to create a ApiKeyController with mocked storage for testing.
func newTestApiKeyController(storage *storagemocks.MockStorage) *ApiKeyController {
	gw := &gatewaymocks.MockGateway{}
	gw.On("SyncAPIKey", mock.Anything).Return(nil)
	gw.On("DeleteAPIKey", mock.Anything).Return(nil)

	c, _ := NewApiKeyController(&ApiKeyControllerOption{
		Storage: storage,
		Gw:      gw,
	})

	return c
}

// testApiKey is a helper to create a basic ApiKey object for tests.
func testApiKey(id string, phase v1.ApiKeyPhase) *v1.ApiKey {
	apiKey := &v1.ApiKey{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-api_key-" + id,
		},
		Spec: &v1.ApiKeySpec{},
		Status: &v1.ApiKeyStatus{
			SkValue: "sk-1234567890",
		},
	}
	if phase != "" { // Only set status if phase is provided.
		apiKey.Status = &v1.ApiKeyStatus{Phase: phase}
	}
	return apiKey
}

// testApiKeyWithDeletionTimestamp is a helper to create a ApiKey object marked for deletion.
func testApiKeyWithDeletionTimestamp(id string, phase v1.ApiKeyPhase) *v1.ApiKey {
	apiKey := testApiKey(id, phase)
	apiKey.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return apiKey
}

// --- Tests for the 'sync' method ---

func TestApiKeyController_Sync_Deletion(t *testing.T) {
	apiKeyID := "test-id"

	tests := []struct {
		name      string
		input     *v1.ApiKey
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testApiKeyWithDeletionTimestamp(apiKeyID, v1.ApiKeyPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteApiKey", apiKeyID).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testApiKeyWithDeletionTimestamp(apiKeyID, v1.ApiKeyPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteApiKey", apiKeyID).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=CREATED) -> Set Phase=DELETED (Update success)",
			input: testApiKeyWithDeletionTimestamp(apiKeyID, v1.ApiKeyPhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseDELETED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testApiKeyWithDeletionTimestamp(apiKeyID, v1.ApiKeyPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseDELETED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestApiKeyController(mockStorage)

			err := c.sync(tt.input) // Test sync directly.

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

func TestApiKeyController_Sync_CreateOrUpdate(t *testing.T) {
	apiKeyID := "test-id"

	tests := []struct {
		name      string
		input     *v1.ApiKey
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=CREATED (Update success)",
			input: testApiKey(apiKeyID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update success)",
			input: testApiKey(apiKeyID, v1.ApiKeyPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseCREATED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=CREATED (Update failed)",
			input: testApiKey(apiKeyID, v1.ApiKeyPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseCREATED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=CREATED -> No Change",
			input: testApiKey(apiKeyID, v1.ApiKeyPhaseCREATED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateApiKey or DeleteApiKey.
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testApiKey(apiKeyID, v1.ApiKeyPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateApiKey or DeleteApiKey.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestApiKeyController(mockStorage)

			err := c.sync(tt.input) // Test sync directly.

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

// --- Test for ListKeys ---

func TestApiKeyController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListApiKey", storage.ListOption{}).Return([]v1.ApiKey{
					{ID: "1"}, {ID: "5"}, {ID: "10"},
				}, nil).Once()
			},
			wantKeys: []interface{}{"1", "5", "10"},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListApiKey", storage.ListOption{}).Return([]v1.ApiKey{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListApiKey", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestApiKeyController(mockStorage)

			keys, err := c.ListKeys()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, keys)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantKeys, keys)
			}
			mockStorage.AssertExpectations(t)
		})
	}
}

// --- Test for Reconcile ---

func TestApiKeyController_Reconcile(t *testing.T) {
	apiKeyID := "test-id"

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.ApiKey) error {
		// Check for a condition to simulate failure.
		if obj != nil && obj.Metadata != nil && obj.Metadata.Name == "sync-should-fail" {
			return errors.New("mock sync failed")
		}
		// Simulate successful sync.
		return nil
	}

	tests := []struct {
		name          string
		inputKey      interface{}
		mockSetup     func(*storagemocks.MockStorage)
		useMockSync   bool  // Flag to indicate if the mock syncHandler should be used.
		expectedError error // Expected contained error string for specific checks.
		wantErr       bool
	}{
		{
			name:     "Reconcile success (real sync, no status change)", // Test scenario using default sync handler.
			inputKey: apiKeyID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetApiKey succeeds, apiKey is already in the desired state.
				s.On("GetApiKey", apiKeyID).Return(testApiKey(apiKeyID, v1.ApiKeyPhaseCREATED), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: apiKeyID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetApiKey succeeds, apiKey needs status update.
				s.On("GetApiKey", apiKeyID).Return(testApiKey(apiKeyID, v1.ApiKeyPhasePENDING), nil).Once()
				// The real 'sync' method expects UpdateApiKey to be called.
				s.On("UpdateApiKey", apiKeyID, mock.MatchedBy(func(r *v1.ApiKey) bool {
					return r.Status != nil && r.Status.Phase == v1.ApiKeyPhaseCREATED
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: apiKeyID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetApiKey succeeds.
				s.On("GetApiKey", apiKeyID).Return(testApiKey(apiKeyID, v1.ApiKeyPhaseCREATED), nil).Once()
				// No further storage calls expected by Reconcile before calling syncHandler.
			},
			useMockSync: true, // Override with mockSyncHandler.
			wantErr:     false,
		},
		{
			name:     "Invalid key type",
			inputKey: 123,
			mockSetup: func(s *storagemocks.MockStorage) {
				// No storage calls expected.
			},
			useMockSync:   false, // Fails before sync handler.
			wantErr:       true,
			expectedError: errors.New("failed to assert key to apiKeyID"),
		},
		{
			name:     "GetApiKey returns error",
			inputKey: apiKeyID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetApiKey to return an error.
				s.On("GetApiKey", apiKeyID).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetApiKey to be propagated.
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: apiKeyID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetApiKey succeeds, providing the apiKey that triggers mock failure.
				apiKey := testApiKey(apiKeyID, v1.ApiKeyPhaseCREATED)
				apiKey.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetApiKey", apiKeyID).Return(apiKey, nil).Once()
			},
			useMockSync: true, // Use the mock handler.
			wantErr:     true, // Expect error from mock sync handler to be propagated.
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			if tt.mockSetup != nil {
				tt.mockSetup(mockStorage)
			}

			// Create controller using the helper.
			c := newTestApiKeyController(mockStorage)

			// Override syncHandler if the test case requires the mock.
			if tt.useMockSync {
				c.syncHandler = mockSyncHandler
			}

			// Directly call the Reconcile method.
			err := c.Reconcile(tt.inputKey)

			// Assertions.
			if tt.wantErr {
				assert.Error(t, err)
				if tt.expectedError != nil {
					// Use Contains for checking wrapped errors.
					assert.Contains(t, err.Error(), tt.expectedError.Error())
				}
			} else {
				assert.NoError(t, err)
			}
			// Verify mock expectations.
			mockStorage.AssertExpectations(t)
		})
	}
}
