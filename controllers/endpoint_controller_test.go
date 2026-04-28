package controllers

import (
	"strconv"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	gatewaymocks "github.com/neutree-ai/neutree/internal/gateway/mocks"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	orchestratormocks "github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newTestEndpointController(store *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) *EndpointController {
	orchestrator.NewOrchestrator = func(opts orchestrator.Options) (orchestrator.Orchestrator, error) {
		return o, nil
	}

	gw := &gatewaymocks.MockGateway{}
	gw.On("SyncEndpoint", mock.Anything).Return(nil)
	gw.On("DeleteEndpoint", mock.Anything).Return(nil)
	gw.On("GetEndpointServeUrl", mock.Anything).Return("", nil)

	c, _ := NewEndpointController(&EndpointControllerOption{Storage: store, Gw: gw})
	return c
}

func ep(id int, phase v1.EndpointPhase) *v1.Endpoint {
	e := &v1.Endpoint{
		ID: id,
		Metadata: &v1.Metadata{
			Name:      "test-endpoint-" + strconv.Itoa(id),
			Workspace: "default",
		},
		Spec: &v1.EndpointSpec{
			Cluster: "test-cluster",
			Engine:  &v1.EndpointEngineSpec{Engine: "test-engine", Version: "1.0.0"},
			Model:   &v1.ModelSpec{Registry: "test-model-registry", Name: "test-model"},
		},
	}
	if phase != "" {
		e.Status = &v1.EndpointStatus{Phase: phase}
	}
	return e
}

func epDel(id int, phase v1.EndpointPhase) *v1.Endpoint {
	e := ep(id, phase)
	e.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
	return e
}

/* ---------- Deletion ---------- */

func TestEndpointController_Sync_Deletion(t *testing.T) {
	id := 1
	tests := []struct {
		name    string
		in      *v1.Endpoint
		setup   func(*storagemocks.MockStorage)
		wantErr bool
	}{
		{
			name: "delete ok",
			in:   epDel(id, v1.EndpointPhaseDELETED),
			setup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEndpoint", strconv.Itoa(id)).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "delete fail",
			in:   epDel(id, v1.EndpointPhaseDELETED),
			setup: func(s *storagemocks.MockStorage) {
				s.On("DeleteEndpoint", strconv.Itoa(id)).Return(assert.AnError)
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			mo := &orchestratormocks.MockOrchestrator{}
			tt.setup(ms)
			c := newTestEndpointController(ms, mo)
			err := c.sync(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
			mo.AssertExpectations(t)
		})
	}
}

/* ---------- Create / Update ---------- */

func TestEndpointController_Sync_CreateUpdate(t *testing.T) {
	id := 1
	cluster := v1.Cluster{Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "default"}}

	okStatus := &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}

	tests := []struct {
		name    string
		in      *v1.Endpoint
		setup   func(*storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
		wantErr bool
	}{
		{
			name: "create ok",
			in:   ep(id, ""),
			setup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Maybe()
				o.On("CreateEndpoint", mock.Anything).Return(nil)
				o.On("GetEndpointStatus", mock.Anything).Return(okStatus, nil)
				s.On("UpdateEndpoint", strconv.Itoa(id), mock.Anything).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "update status fail - logged but not returned",
			in:   ep(id, ""),
			setup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Maybe()
				o.On("CreateEndpoint", mock.Anything).Return(nil)
				o.On("GetEndpointStatus", mock.Anything).Return(okStatus, nil)

				// Defer block catches updateStatus error and logs it without returning
				s.On("UpdateEndpoint", strconv.Itoa(id), mock.Anything).Return(assert.AnError)
			},
			wantErr: false, // Changed: defer block logs error but doesn't return it
		},
		{
			name: "always create even if already running",
			in:   ep(id, v1.EndpointPhaseRUNNING),
			setup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Maybe()
				o.On("CreateEndpoint", mock.Anything).Return(nil)
				o.On("GetEndpointStatus", mock.Anything).Return(okStatus, nil)
			},
			wantErr: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			mo := &orchestratormocks.MockOrchestrator{}
			tt.setup(ms, mo)
			c := newTestEndpointController(ms, mo)
			err := c.sync(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
			mo.AssertExpectations(t)
		})
	}
}

/* ---------- Reconcile ---------- */

func TestEndpointController_Reconcile(t *testing.T) {
	id := 1
	tests := []struct {
		name    string
		key     interface{}
		setup   func(*storagemocks.MockStorage)
		wantErr bool
	}{
		{
			name: "ok",
			key:  ep(id, v1.EndpointPhaseRUNNING),
			setup: func(s *storagemocks.MockStorage) {
			},
			wantErr: false,
		},
		{
			name:    "invalid key",
			key:     "a",
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			if tt.setup != nil {
				tt.setup(ms)
			}
			c := &EndpointController{storage: ms, syncHandler: func(*v1.Endpoint) error { return nil }}
			err := c.Reconcile(tt.key)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
		})
	}
}

func Test_UpdateStatusOnError(t *testing.T) {
	newEndpoint := func() *v1.Endpoint {
		return &v1.Endpoint{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:      "test-endpoint",
				Workspace: "default",
			},
			Spec: &v1.EndpointSpec{
				Cluster: "test",
			},
			Status: &v1.EndpointStatus{},
		}
	}

	forceDeleteAnnotations := map[string]string{
		"neutree.ai/force-delete": "true",
	}

	testErr := errors.New("test error")
	tests := []struct {
		name      string
		input     func() *v1.Endpoint
		inputErr  error
		mockSetup func(*storagemocks.MockStorage, *orchestratormocks.MockOrchestrator)
	}{
		{
			name:     "update status succeed",
			input:    func() *v1.Endpoint { return newEndpoint() },
			inputErr: nil,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase: v1.EndpointPhaseRUNNING,
				}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseRUNNING &&
						ep.Status.ErrorMessage == ""
				})).Return(nil)
			},
		},
		{
			name: "process force delete first",
			input: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				ep.SetAnnotations(forceDeleteAnnotations)
				return ep
			},
			inputErr: nil,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseDELETED &&
						ep.Status.ErrorMessage == ""
				})).Return(nil)
			},
		},
		{
			name: "process force delete first even with error",
			input: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				ep.SetAnnotations(forceDeleteAnnotations)
				return ep
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseDELETED &&
						ep.Status.ErrorMessage == ""
				})).Return(nil)
			},
		},
		{
			// R4 fallback: when observed status retrieval fails AND we're
			// deleting AND syncErr is set, surface as Deleting + syncErr.
			name: "process reconcile error without force delete (observed unavailable, fallback to Deleting)",
			input: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				return ep
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				// observed retrieval fails: cluster not found
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseDELETING &&
						ep.Status.ErrorMessage == "test error"
				})).Return(nil)
			},
		},
		{
			// R4 fallback: when observed status retrieval fails AND not deleting
			// AND syncErr is set, surface as Failed + syncErr.
			name: "process reconcile error without deletion (observed unavailable, fallback to Failed)",
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				// observed retrieval fails: cluster not found
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseFAILED &&
						ep.Status.ErrorMessage == "test error"
				})).Return(nil)
			},
		},
		{
			name: "get actual failed status when no reconcile error",
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: nil,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase: v1.EndpointPhaseFAILED,
				}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseFAILED &&
						ep.Status.ErrorMessage == ""
				})).Return(nil)
			},
		},
		{
			name: "get actual status failed will not update status",
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: nil,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase: v1.EndpointPhaseFAILED,
				}, assert.AnError)
			},
		},

		// NEU-421 R4: phase derived from observed reality even when sync errored.
		// syncErr is recorded into ErrorMessage so the operator still sees the
		// failed reconcile reason, but the phase reflects what the orchestrator
		// reports — mirroring how cluster controller decouples phase from err.
		{
			name: "R4: observed Running with sync error -> Running phase, syncErr in errorMessage",
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase: v1.EndpointPhaseRUNNING,
				}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseRUNNING &&
						ep.Status.ErrorMessage == "test error"
				})).Return(nil)
			},
		},
		{
			name: "R4: observed Paused with sync error from PauseEndpoint -> Paused phase",
			// NEU-421 bug fix: pause failed because of model registry, but
			// orchestrator reports Paused (replicas=0, no pods); we surface
			// Paused, not Failed.
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: errors.New("failed to pause: model registry not found"),
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase: v1.EndpointPhasePAUSED,
				}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhasePAUSED &&
						ep.Status.ErrorMessage == "failed to pause: model registry not found"
				})).Return(nil)
			},
		},
		{
			name: "R4: observed errorMessage takes precedence over syncErr when both present",
			// e.g., observed reports Deploying with a specific reason; we
			// preserve that and don't overwrite with syncErr — observed
			// is more specific and actionable.
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{{}}, nil)
				o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{
					Phase:        v1.EndpointPhaseDEPLOYING,
					ErrorMessage: "Endpoint deploying in progress: ContainerCreating",
				}, nil)
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseDEPLOYING &&
						ep.Status.ErrorMessage == "Endpoint deploying in progress: ContainerCreating"
				})).Return(nil)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockOrchestrator := &orchestratormocks.MockOrchestrator{}
			tt.mockSetup(mockStorage, mockOrchestrator)
			c := newTestEndpointController(mockStorage, mockOrchestrator)
			c.updateStatusOnError(tt.input(), tt.inputErr)
			mockStorage.AssertExpectations(t)
			mockOrchestrator.AssertExpectations(t)
		})
	}

}

func Test_ShouldUpdateStatus(t *testing.T) {
	tests := []struct {
		name      string
		oldStatus *v1.EndpointStatus
		newStatus *v1.EndpointStatus
		want      bool
	}{
		{
			name:      "nil old status",
			oldStatus: nil,
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
			want:      true,
		},
		{
			name:      "both nil statuses",
			oldStatus: nil,
			newStatus: nil,
			want:      false,
		},
		{
			name:      "new status nil",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
			newStatus: nil,
			want:      false,
		},
		{
			name:      "different phase",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhasePENDING},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
			want:      true,
		},
		{
			name:      "same phase, different error message",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ErrorMessage: "old error"},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ErrorMessage: "new error"},
			want:      true,
		},
		{
			name:      "same phase and error message",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ErrorMessage: "same error"},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ErrorMessage: "same error"},
			want:      false,
		},

		{
			name:      "same phase, different service URL",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServiceURL: "old-url"},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServiceURL: "new-url"},
			want:      true,
		},
		{
			name:      "same phase and service URL",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServiceURL: "same-url"},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServiceURL: "same-url"},
			want:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &EndpointController{}
			got := c.shouldUpdateStatus(&v1.Endpoint{Status: tt.oldStatus}, tt.newStatus)
			if got != tt.want {
				t.Errorf("shouldUpdateStatus() = %v, want %v", got, tt.want)
			}
		})
	}
}
