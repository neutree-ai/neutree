package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newTestEndpointController is a helper to create a EndpointController with mocked storage for testing.
func newTestEndpointController(storage *storagemocks.MockStorage) *EndpointController {
	c, _ := NewEndpointController(&EndpointControllerOption{
		Storage: storage,
		Workers: 1,
	})
	// Use a predictable queue for testing.
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "endpoint-test"})
	return c
}

// testEndpoint is a helper to create a basic Endpoint object for tests.
func testEndpoint(id int, phase v1.EndpointPhase) *v1.Endpoint {
	endpoint := &v1.Endpoint{
		ID: id,
		Metadata: &v1.Metadata{
			Name: "test-endpoint-" + strconv.Itoa(id),
		},
		Spec: &v1.EndpointSpec{},
	}
	if phase != "" { // Only set status if phase is provided.
		endpoint.Status = &v1.EndpointStatus{Phase: phase}
	}
	return endpoint
}

// testEndpointWithDeletionTimestamp is a helper to create a Endpoint object marked for deletion.
func testEndpointWithDeletionTimestamp(id int, phase v1.EndpointPhase) *v1.Endpoint {
	endpoint := testEndpoint(id, phase)
	endpoint.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return endpoint
}

// --- Tests for the 'sync' method ---

func TestEndpointController_Sync_Deletion(t *testing.T) {
	endpointID := 1
	endpointIDStr := strconv.Itoa(endpointID)

	tests := []struct {
		name      string
		input     *v1.Endpoint
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleting (Phase=DELETED) -> Deleted (DB delete success)",
			input: testEndpointWithDeletionTimestamp(endpointID, v1.EndpointPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEndpoint", endpointIDStr).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=DELETED) -> Error (DB delete failed)",
			input: testEndpointWithDeletionTimestamp(endpointID, v1.EndpointPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEndpoint", endpointIDStr).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Deleting (Phase=RUNNING) -> Set Phase=DELETED (Update success)",
			input: testEndpointWithDeletionTimestamp(endpointID, v1.EndpointPhaseRUNNING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseDELETED && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Deleting (Phase=PENDING) -> Set Phase=DELETED (Update failed)",
			input: testEndpointWithDeletionTimestamp(endpointID, v1.EndpointPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseDELETED
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEndpointController(mockStorage)

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

func TestEndpointController_Sync_CreateOrUpdate(t *testing.T) {
	endpointID := 1
	endpointIDStr := strconv.Itoa(endpointID)

	tests := []struct {
		name      string
		input     *v1.Endpoint
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "No Status -> Set Phase=RUNNING (Update success)",
			input: testEndpoint(endpointID, ""),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseRUNNING && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=RUNNING (Update success)",
			input: testEndpoint(endpointID, v1.EndpointPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseRUNNING && r.Status.ErrorMessage == ""
				})).Return(nil).Once()
			},
			wantErr: false,
		},
		{
			name:  "Phase=PENDING -> Set Phase=RUNNING (Update failed)",
			input: testEndpoint(endpointID, v1.EndpointPhasePENDING),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseRUNNING
				})).Return(assert.AnError).Once()
			},
			wantErr: true,
		},
		{
			name:  "Phase=RUNNING -> No Change",
			input: testEndpoint(endpointID, v1.EndpointPhaseRUNNING),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateEndpoint or DeleteEndpoint.
			},
			wantErr: false,
		},
		{
			name:  "Phase=DELETED (no deletionTimestamp) -> No Change",
			input: testEndpoint(endpointID, v1.EndpointPhaseDELETED),
			mockSetup: func(s *storagemocks.MockStorage) {
				// Expect no calls to UpdateEndpoint or DeleteEndpoint.
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEndpointController(mockStorage)

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

func TestEndpointController_ListKeys(t *testing.T) {
	tests := []struct {
		name      string
		mockSetup func(*storagemocks.MockStorage)
		wantKeys  []interface{}
		wantErr   bool
	}{
		{
			name: "List success",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", storage.ListOption{}).Return([]v1.Endpoint{
					{ID: 1}, {ID: 5}, {ID: 10},
				}, nil).Once()
			},
			wantKeys: []interface{}{1, 5, 10},
			wantErr:  false,
		},
		{
			name: "List returns empty",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", storage.ListOption{}).Return([]v1.Endpoint{}, nil).Once()
			},
			wantKeys: []interface{}{},
			wantErr:  false,
		},
		{
			name: "List returns error",
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", storage.ListOption{}).Return(nil, assert.AnError).Once()
			},
			wantKeys: nil,
			wantErr:  true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			tt.mockSetup(mockStorage)
			c := newTestEndpointController(mockStorage)

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

func TestEndpointController_Reconcile(t *testing.T) {
	endpointID := 1
	endpointIDStr := strconv.Itoa(endpointID)

	// mockSyncHandler provides a controllable sync function for Reconcile tests.
	mockSyncHandler := func(obj *v1.Endpoint) error {
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
			inputKey: endpointID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEndpoint succeeds, endpoint is already in the desired state.
				s.On("GetEndpoint", endpointIDStr).Return(testEndpoint(endpointID, v1.EndpointPhaseRUNNING), nil).Once()
				// The real 'sync' method expects no further storage calls here.
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (real sync, status updated)", // Test scenario using default sync handler.
			inputKey: endpointID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEndpoint succeeds, endpoint needs status update.
				s.On("GetEndpoint", endpointIDStr).Return(testEndpoint(endpointID, v1.EndpointPhasePENDING), nil).Once()
				// The real 'sync' method expects UpdateEndpoint to be called.
				s.On("UpdateEndpoint", endpointIDStr, mock.MatchedBy(func(r *v1.Endpoint) bool {
					return r.Status != nil && r.Status.Phase == v1.EndpointPhaseRUNNING
				})).Return(nil).Once()
			},
			useMockSync: false, // Use the default c.sync via syncHandler.
			wantErr:     false,
		},
		{
			name:     "Reconcile success (mock sync)", // Test Reconcile isolation using mock handler.
			inputKey: endpointID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEndpoint succeeds.
				s.On("GetEndpoint", endpointIDStr).Return(testEndpoint(endpointID, v1.EndpointPhaseRUNNING), nil).Once()
				// No further storage calls expected by Reconcile before calling syncHandler.
			},
			useMockSync: true, // Override with mockSyncHandler.
			wantErr:     false,
		},
		{
			name:     "Invalid key type",
			inputKey: "not-an-int",
			mockSetup: func(s *storagemocks.MockStorage) {
				// No storage calls expected.
			},
			useMockSync:   false, // Fails before sync handler.
			wantErr:       true,
			expectedError: errors.New("failed to assert key to endpointID"),
		},
		{
			name:     "GetEndpoint returns error",
			inputKey: endpointID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// Mock GetEndpoint to return an error.
				s.On("GetEndpoint", endpointIDStr).Return(nil, assert.AnError).Once()
			},
			useMockSync: false, // Fails before sync handler.
			wantErr:     true,  // Expect error from GetEndpoint to be propagated.
		},
		{
			name:     "Sync handler returns error (mock sync)",
			inputKey: endpointID,
			mockSetup: func(s *storagemocks.MockStorage) {
				// GetEndpoint succeeds, providing the endpoint that triggers mock failure.
				endpoint := testEndpoint(endpointID, v1.EndpointPhaseRUNNING)
				endpoint.Metadata.Name = "sync-should-fail" // Condition for mockSyncHandler failure.
				s.On("GetEndpoint", endpointIDStr).Return(endpoint, nil).Once()
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
			c := newTestEndpointController(mockStorage)

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
