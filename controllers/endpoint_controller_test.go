package controllers

import (
	"strconv"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	orchestratormocks "github.com/neutree-ai/neutree/internal/orchestrator/mocks"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"k8s.io/client-go/util/workqueue"
)

func newTestEndpointController(store *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) *EndpointController {
	orchestrator.NewOrchestrator = func(opts orchestrator.Options) (orchestrator.Orchestrator, error) {
		return o, nil
	}
	c, _ := NewEndpointController(&EndpointControllerOption{Storage: store, Workers: 1})
	c.baseController.queue = workqueue.NewRateLimitingQueueWithConfig(
		workqueue.DefaultControllerRateLimiter(),
		workqueue.RateLimitingQueueConfig{Name: "endpoint-test"},
	)
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
	imageRegistry := v1.ImageRegistry{Metadata: &v1.Metadata{Name: "default-registry", Workspace: "default"}}
	engine := v1.Engine{Metadata: &v1.Metadata{Name: "test-engine"}}
	modelRegistry := v1.ModelRegistry{Metadata: &v1.Metadata{Name: "test-model-registry"}}

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
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{imageRegistry}, nil).Maybe()
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{engine}, nil).Maybe()
				s.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil).Maybe()
				o.On("ConnectEndpointModel", mock.Anything).Return(nil)
				o.On("CreateEndpoint", mock.Anything).Return(okStatus, nil)
				s.On("UpdateEndpoint", strconv.Itoa(id), mock.Anything).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "update fail",
			in:   ep(id, ""),
			setup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Maybe()
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{imageRegistry}, nil).Maybe()
				s.On("ListEngine", mock.Anything).Return([]v1.Engine{engine}, nil).Maybe()
				s.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{modelRegistry}, nil).Maybe()
				o.On("ConnectEndpointModel", mock.Anything).Return(nil)
				o.On("CreateEndpoint", mock.Anything).Return(okStatus, nil)
				s.On("UpdateEndpoint", strconv.Itoa(id), mock.Anything).Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name: "running health ok",
			in:   ep(id, v1.EndpointPhaseRUNNING),
			setup: func(s *storagemocks.MockStorage, o *orchestratormocks.MockOrchestrator) {
				s.On("ListCluster", mock.Anything).Return([]v1.Cluster{cluster}, nil).Maybe()
				s.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{imageRegistry}, nil).Maybe()
				o.On("CreateEndpoint", mock.Anything).Return(okStatus, nil)
				o.On("ConnectEndpointModel", mock.Anything).Return(nil)
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

/* ---------- ListKeys ---------- */

func TestEndpointController_ListKeys(t *testing.T) {
	tests := []struct {
		name    string
		setup   func(*storagemocks.MockStorage)
		wantErr bool
	}{
		{
			name: "ok",
			setup: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", storage.ListOption{}).Return([]v1.Endpoint{{ID: 1}, {ID: 3}}, nil)
			},
			wantErr: false,
		},
		{
			name: "err",
			setup: func(s *storagemocks.MockStorage) {
				s.On("ListEndpoint", storage.ListOption{}).Return(nil, assert.AnError)
			},
			wantErr: true,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			tt.setup(ms)
			c := &EndpointController{storage: ms}
			_, err := c.ListKeys()
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
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
			key:  id,
			setup: func(s *storagemocks.MockStorage) {
				s.On("GetEndpoint", strconv.Itoa(id)).Return(ep(id, v1.EndpointPhaseRUNNING), nil)
			},
			wantErr: false,
		},
		{
			name:    "invalid key",
			key:     "a",
			wantErr: true,
		},
		{
			name: "get fail",
			key:  id,
			setup: func(s *storagemocks.MockStorage) {
				s.On("GetEndpoint", strconv.Itoa(id)).Return(nil, assert.AnError)
			},
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
