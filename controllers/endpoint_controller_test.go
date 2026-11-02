package controllers

import (
	"fmt"
	"net/http"
	"net/http/httptest"
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
	"github.com/stretchr/testify/require"
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

func stringPtr(v string) *string { return &v }

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
			name: "process reconcile error without force delete",
			input: func() *v1.Endpoint {
				ep := newEndpoint()
				ep.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)
				return ep
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("UpdateEndpoint", "1", mock.MatchedBy(func(ep *v1.Endpoint) bool {
					return ep.Status != nil &&
						ep.Status.Phase == v1.EndpointPhaseDELETING &&
						ep.Status.ErrorMessage == "test error"
				})).Return(nil)
			},
		},
		{
			name: "process reconcile error without force delete and with deletion timestamp",
			input: func() *v1.Endpoint {
				return newEndpoint()
			},
			inputErr: testErr,
			mockSetup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
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
	endpointResources := func(memoryMiB, coreUnits int64) *v1.EndpointResourceStatus {
		return &v1.EndpointResourceStatus{
			Replicas: []v1.ReplicaDeviceAllocation{
				{
					InstanceID: "pod-1",
					ReplicaID:  "replica-1",
					NodeID:     "node-1",
					Devices: []v1.DeviceAllocation{
						{
							UUID:      "GPU-1",
							Product:   "Tesla-T4",
							MemoryMiB: memoryMiB,
							CoreUnits: coreUnits,
							NodeID:    "node-1",
						},
					},
				},
			},
			Summary: &v1.EndpointResourceSummary{
				Products: map[v1.AcceleratorProduct]*v1.ProductUsage{
					"Tesla-T4": {
						MemoryMiB: memoryMiB,
						CoreUnits: coreUnits,
					},
				},
			},
		}
	}

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
		{
			name:      "same phase, resources added",
			oldStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
			newStatus: &v1.EndpointStatus{
				Phase:     v1.EndpointPhaseRUNNING,
				Resources: endpointResources(8192, 50),
			},
			want: true,
		},
		{
			name: "same phase, different model download completion hash",
			oldStatus: &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: stringPtr("old-hash"),
			},
			newStatus: &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: stringPtr("new-hash"),
			},
			want: true,
		},
		{
			name: "same phase and same model download metadata",
			oldStatus: &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: stringPtr("same-hash"),
			},
			newStatus: &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: stringPtr("same-hash"),
			},
			want: false,
		},
		{
			name: "same phase, same resources",
			oldStatus: &v1.EndpointStatus{
				Phase:     v1.EndpointPhaseRUNNING,
				Resources: endpointResources(8192, 50),
			},
			newStatus: &v1.EndpointStatus{
				Phase:     v1.EndpointPhaseRUNNING,
				Resources: endpointResources(8192, 50),
			},
			want: false,
		},
		{
			name: "same phase and omitted model download metadata that will be preserved",
			oldStatus: &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: stringPtr("same-hash"),
			},
			newStatus: &v1.EndpointStatus{
				Phase: v1.EndpointPhaseDEPLOYING,
			},
			want: false,
		},
		{
			name: "same phase, omitted resources that will be preserved",
			oldStatus: &v1.EndpointStatus{
				Phase:     v1.EndpointPhaseRUNNING,
				Resources: endpointResources(8192, 50),
			},
			newStatus: &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING},
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

func TestEndpointControllerPrepareStatusForUpdatePreservesResourcesWhenNewStatusOmitsThem(t *testing.T) {
	oldResources := endpointResourcesForTest(8192, 50)
	endpoint := &v1.Endpoint{
		Status: &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseRUNNING,
			Resources: oldResources,
		},
	}
	newStatus := &v1.EndpointStatus{Phase: v1.EndpointPhaseFAILED}

	(&EndpointController{}).prepareStatusForUpdate(endpoint, newStatus)

	assert.Equal(t, oldResources, newStatus.Resources)
}

func TestEndpointControllerPrepareStatusForUpdateKeepsNewResourcesWhenPresent(t *testing.T) {
	oldResources := endpointResourcesForTest(8192, 50)
	newResources := endpointResourcesForTest(16384, 100)
	endpoint := &v1.Endpoint{
		Status: &v1.EndpointStatus{
			Phase:     v1.EndpointPhaseRUNNING,
			Resources: oldResources,
		},
	}
	newStatus := &v1.EndpointStatus{
		Phase:     v1.EndpointPhaseRUNNING,
		Resources: newResources,
	}

	(&EndpointController{}).prepareStatusForUpdate(endpoint, newStatus)

	assert.Equal(t, newResources, newStatus.Resources)
}

func endpointResourcesForTest(memoryMiB, coreUnits int64) *v1.EndpointResourceStatus {
	return &v1.EndpointResourceStatus{
		Replicas: []v1.ReplicaDeviceAllocation{
			{
				InstanceID: "pod-1",
				ReplicaID:  "replica-1",
				NodeID:     "node-1",
				Devices: []v1.DeviceAllocation{
					{
						UUID:      "GPU-1",
						Product:   "Tesla-T4",
						MemoryMiB: memoryMiB,
						CoreUnits: coreUnits,
						NodeID:    "node-1",
					},
				},
			},
		},
		Summary: &v1.EndpointResourceSummary{
			Products: map[v1.AcceleratorProduct]*v1.ProductUsage{
				"Tesla-T4": {
					MemoryMiB: memoryMiB,
					CoreUnits: coreUnits,
				},
			},
		},
	}
}

func Test_PrepareStatusForUpdate_PreservesOmittedModelDownloadHash(t *testing.T) {
	endpoint := ep(1, v1.EndpointPhaseDEPLOYING)
	endpoint.Status.ModelDownloadCompletedHash = stringPtr("completed-hash")

	status := &v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING}
	c := &EndpointController{}
	c.prepareStatusForUpdate(endpoint, status)

	assert.NotNil(t, status.ModelDownloadCompletedHash)
	assert.Equal(t, "completed-hash", *status.ModelDownloadCompletedHash)
	assert.False(t, c.shouldUpdateStatus(endpoint, &v1.EndpointStatus{Phase: v1.EndpointPhaseDEPLOYING}))
}

func Test_PrepareStatusForUpdate_UsesExplicitModelDownloadHash(t *testing.T) {
	tests := []struct {
		name     string
		newHash  *string
		expected string
	}{
		{
			name:     "empty hash clears stale completion",
			newHash:  stringPtr(""),
			expected: "",
		},
		{
			name:     "new hash updates completion",
			newHash:  stringPtr("new-hash"),
			expected: "new-hash",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			endpoint := ep(1, v1.EndpointPhaseDEPLOYING)
			endpoint.Status.ModelDownloadCompletedHash = stringPtr("old-hash")

			status := &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: tt.newHash,
			}
			c := &EndpointController{}
			c.prepareStatusForUpdate(endpoint, status)

			assert.NotNil(t, status.ModelDownloadCompletedHash)
			assert.Equal(t, tt.expected, *status.ModelDownloadCompletedHash)
			assert.True(t, c.shouldUpdateStatus(endpoint, &v1.EndpointStatus{
				Phase:                      v1.EndpointPhaseDEPLOYING,
				ModelDownloadCompletedHash: tt.newHash,
			}))
		})
	}
}

func TestFetchServedModels(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		want    []string
		wantErr bool
	}{
		{name: "ordered unique models", body: `{"data":[{"id":"alias-b"},{"id":"alias-a"},{"id":"alias-b"}]}`, want: []string{"alias-b", "alias-a"}},
		{name: "empty list", body: `{"data":[]}`, want: []string{}},
		{name: "keeps exact model identity", body: `{"data":[{"id":" model "}]}`, want: []string{" model "}},
		{name: "blank ids are skipped", body: `{"data":[{"id":""},{"id":"alias-a"},{"id":" "}]}`, want: []string{"alias-a"}},
		{name: "only blank ids", body: `{"data":[{"id":""}]}`, want: []string{}},
		{name: "server failure", status: http.StatusServiceUnavailable, body: `{"data":[]}`, wantErr: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, "/team%2Fa/model%3Fname/v1/models", r.URL.EscapedPath())
				assert.Equal(t, http.MethodGet, r.Method)
				if tc.status != 0 {
					w.WriteHeader(tc.status)
				}
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}
			endpoint := &v1.Endpoint{Metadata: &v1.Metadata{Workspace: "team/a", Name: "model?name"}}
			got, err := fetchServedModels(cluster, endpoint)
			if tc.wantErr {
				require.Error(t, err)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

func TestServedModelsProbeBypassesEnvironmentProxy(t *testing.T) {
	transport, ok := servedModelsClient.Transport.(*http.Transport)
	require.True(t, ok)
	assert.Nil(t, transport.Proxy)
}

func TestFetchServedModelsDoesNotFollowRedirect(t *testing.T) {
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		t.Error("model discovery followed a redirect")
	}))
	defer target.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, target.URL, http.StatusFound)
	}))
	defer server.Close()
	cluster := &v1.Cluster{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}
	_, err := fetchServedModels(cluster, ep(1, v1.EndpointPhaseRUNNING))
	require.Error(t, err)
}

func TestEndpointServedModelsStatus(t *testing.T) {
	c := &EndpointController{}
	endpoint := ep(1, v1.EndpointPhaseRUNNING)
	endpoint.Status.ServedModels = []string{"first", "second"}
	cases := []struct {
		name    string
		models  []string
		want    []string
		changed bool
	}{
		{name: "preserve observation when not refreshed", models: nil, want: []string{"first", "second"}},
		{name: "same models", models: []string{"first", "second"}, want: []string{"first", "second"}},
		{name: "first model changed", models: []string{"second", "first"}, want: []string{"second", "first"}, changed: true},
		{name: "successful empty collection", models: []string{}, want: []string{}, changed: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status := &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServedModels: tc.models}
			assert.Equal(t, tc.changed, c.shouldUpdateStatus(endpoint, status))
			c.prepareStatusForUpdate(endpoint, status)
			assert.Equal(t, tc.want, status.ServedModels)
		})
	}
	assert.False(t, c.shouldUpdateStatus(ep(1, v1.EndpointPhaseRUNNING), &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ServedModels: []string{}}))
}

func TestEndpointObservedSpecHash(t *testing.T) {
	c := &EndpointController{}
	endpoint := ep(1, v1.EndpointPhaseRUNNING)
	endpoint.Status.ServedModels = []string{"first"}
	endpoint.Status.ObservedSpecHash = "recorded"

	// The fingerprint does not gate the last observation: a status that carries
	// no names keeps what discovery found, whichever spec it was derived for.
	status := &v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING, ObservedSpecHash: "other"}
	c.prepareStatusForUpdate(endpoint, status)
	assert.Equal(t, []string{"first"}, status.ServedModels)

	// A new hash alone is enough to persist the status.
	assert.True(t, c.shouldUpdateStatus(endpoint, &v1.EndpointStatus{
		Phase:            v1.EndpointPhaseRUNNING,
		ServedModels:     []string{"first"},
		ObservedSpecHash: "new",
	}))

	// Same names and same hash are not a change.
	assert.False(t, c.shouldUpdateStatus(endpoint, &v1.EndpointStatus{
		Phase:            v1.EndpointPhaseRUNNING,
		ServedModels:     []string{"first"},
		ObservedSpecHash: "recorded",
	}))

	// A status written without discovery keeps the fingerprint behind the names
	// it kept.
	failed := c.formatStatus(v1.EndpointPhaseFAILED, assert.AnError)
	c.prepareStatusForUpdate(endpoint, failed)
	assert.Equal(t, []string{"first"}, failed.ServedModels)
	assert.Equal(t, "recorded", failed.ObservedSpecHash)
}

func TestEndpointControllerHoldsHashWhenObservationFails(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = fmt.Fprint(w, `{"data":[{"id":"runtime-alias"}]}`)
	}))
	defer server.Close()

	original := orchestrator.NewOrchestrator
	t.Cleanup(func() { orchestrator.NewOrchestrator = original })
	orchestrator.NewOrchestrator = func(opts orchestrator.Options) (orchestrator.Orchestrator, error) {
		o := &orchestratormocks.MockOrchestrator{}
		o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{Phase: v1.EndpointPhaseRUNNING}, nil)

		return o, nil
	}

	store := &storagemocks.MockStorage{}
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Spec:   &v1.ClusterSpec{Type: v1.KubernetesClusterType},
		Status: &v1.ClusterStatus{DashboardURL: server.URL},
	}}, nil)
	// The status is still collected from whatever succeeds, but a failed
	// observation leaves the fingerprint on the spec it was last recorded for.
	gw := &gatewaymocks.MockGateway{}
	gw.On("GetEndpointServeUrl", mock.Anything).Return("", assert.AnError)

	c, _ := NewEndpointController(&EndpointControllerOption{Storage: store, Gw: gw})

	endpoint := ep(1, v1.EndpointPhaseRUNNING)
	endpoint.Status.ObservedSpecHash = "hash-of-a-previous-spec"

	status, err := c.getActualStatus(endpoint)
	require.NoError(t, err)
	assert.Equal(t, []string{"runtime-alias"}, status.ServedModels)
	assert.Equal(t, "hash-of-a-previous-spec", status.ObservedSpecHash)
}

func TestEndpointControllerObservesServedModels(t *testing.T) {
	original := orchestrator.NewOrchestrator
	t.Cleanup(func() { orchestrator.NewOrchestrator = original })
	cases := []struct {
		name string
		// recordedHash selects what the endpoint status carries before the call:
		// empty, the current spec hash, or a hash of a different spec.
		recordedHash string
		phase        v1.EndpointPhase
		body         string
		want         []string
		// noRecordedNames drops the previous observation from the status.
		noRecordedNames bool
		// keepRecordedHash asserts the fingerprint was not advanced.
		keepRecordedHash bool
		requests         int
	}{
		{name: "refresh running", phase: v1.EndpointPhaseRUNNING, body: `{"data":[{"id":"runtime-alias"}]}`, want: []string{"runtime-alias"}, requests: 1},
		{name: "failed refresh retains observation", phase: v1.EndpointPhaseRUNNING, body: `{`, want: []string{"previous"}, keepRecordedHash: true, requests: 1},
		{name: "successful empty clears observation", phase: v1.EndpointPhaseRUNNING, body: `{"data":[]}`, want: []string{}, requests: 1},
		{name: "paused does not probe", phase: v1.EndpointPhasePAUSED, want: []string{"previous"}, keepRecordedHash: true},
		{name: "unchanged spec reuses observation", recordedHash: "current", phase: v1.EndpointPhaseRUNNING, want: []string{"previous"}},
		{name: "changed spec probes again", recordedHash: "stale", phase: v1.EndpointPhaseRUNNING, body: `{"data":[{"id":"runtime-alias"}]}`, want: []string{"runtime-alias"}, requests: 1},
		{name: "changed spec keeps hash when probe fails", recordedHash: "stale", phase: v1.EndpointPhaseRUNNING, body: `{`, want: []string{"previous"}, keepRecordedHash: true, requests: 1},
		// An observation of no models is persisted as no names, so the unchanged
		// spec must not read it as a missing observation and probe every reconcile.
		{name: "empty observation is not probed again", recordedHash: "current", noRecordedNames: true, phase: v1.EndpointPhaseRUNNING, want: nil},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			requests := 0
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests++
				assert.Equal(t, "/default/test-endpoint-1/v1/models", r.URL.Path)
				_, _ = fmt.Fprint(w, tc.body)
			}))
			defer server.Close()
			store := &storagemocks.MockStorage{}
			store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType}, Status: &v1.ClusterStatus{DashboardURL: server.URL}}}, nil)
			o := &orchestratormocks.MockOrchestrator{}
			o.On("GetEndpointStatus", mock.Anything).Return(&v1.EndpointStatus{Phase: tc.phase}, nil)
			c := newTestEndpointController(store, o)
			endpoint := ep(1, v1.EndpointPhaseRUNNING)
			if !tc.noRecordedNames {
				endpoint.Status.ServedModels = []string{"previous"}
			}
			recordedHash := ""
			switch tc.recordedHash {
			case "current":
				recordedHash = endpointSpecHash(endpoint)
			case "stale":
				recordedHash = "hash-of-a-different-spec"
			}
			endpoint.Status.ObservedSpecHash = recordedHash

			status, err := c.getActualStatus(endpoint)
			require.NoError(t, err)
			c.prepareStatusForUpdate(endpoint, status)
			assert.Equal(t, tc.phase, status.Phase)
			assert.Equal(t, tc.want, status.ServedModels)
			if tc.keepRecordedHash {
				assert.Equal(t, recordedHash, status.ObservedSpecHash)
			} else {
				assert.Equal(t, endpointSpecHash(endpoint), status.ObservedSpecHash)
			}
			assert.Equal(t, tc.requests, requests)
			store.AssertExpectations(t)
			o.AssertExpectations(t)
		})
	}
}
