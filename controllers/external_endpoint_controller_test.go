package controllers

import (
	"strconv"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	gatewaymocks "github.com/neutree-ai/neutree/internal/gateway/mocks"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newTestExternalEndpointController(store *storagemocks.MockStorage, gw *gatewaymocks.MockGateway) *ExternalEndpointController {
	c, _ := NewExternalEndpointController(&ExternalEndpointControllerOption{Storage: store, Gw: gw})
	return c
}

func ee(id int, phase v1.ExternalEndpointPhase) *v1.ExternalEndpoint {
	e := &v1.ExternalEndpoint{
		ID: id,
		Metadata: &v1.Metadata{
			Name:      "test-ext-ep-" + strconv.Itoa(id),
			Workspace: "default",
		},
		Spec: &v1.ExternalEndpointSpec{},
	}
	if phase != "" {
		e.Status = &v1.ExternalEndpointStatus{Phase: phase}
	}

	return e
}

func eeDel(id int, phase v1.ExternalEndpointPhase) *v1.ExternalEndpoint {
	e := ee(id, phase)
	e.Metadata.DeletionTimestamp = time.Now().Format(time.RFC3339Nano)

	return e
}

/* ---------- Reconcile ---------- */

func TestExternalEndpointController_Reconcile(t *testing.T) {
	tests := []struct {
		name    string
		obj     any
		wantErr bool
	}{
		{
			name:    "valid external endpoint",
			obj:     ee(1, v1.ExternalEndpointPhaseRUNNING),
			wantErr: false,
		},
		{
			name:    "invalid type",
			obj:     "not-an-external-endpoint",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			gw := &gatewaymocks.MockGateway{}
			c := &ExternalEndpointController{
				storage:     ms,
				gw:          gw,
				syncHandler: func(*v1.ExternalEndpoint) error { return nil },
			}
			err := c.Reconcile(tt.obj)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

/* ---------- Deletion ---------- */

func TestExternalEndpointController_Sync_Deletion(t *testing.T) {
	id := 1
	tests := []struct {
		name    string
		in      *v1.ExternalEndpoint
		setup   func(*storagemocks.MockStorage, *gatewaymocks.MockGateway)
		wantErr bool
	}{
		{
			name: "phase=DELETED deletes from DB",
			in:   eeDel(id, v1.ExternalEndpointPhaseDELETED),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				s.On("DeleteExternalEndpoint", strconv.Itoa(id)).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "phase=DELETED delete fails returns error",
			in:   eeDel(id, v1.ExternalEndpointPhaseDELETED),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				s.On("DeleteExternalEndpoint", strconv.Itoa(id)).Return(assert.AnError)
			},
			wantErr: true,
		},
		{
			name: "first deletion calls gateway and updates to DELETED",
			in:   eeDel(id, v1.ExternalEndpointPhasePENDING),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				g.On("DeleteExternalEndpoint", mock.Anything).Return(nil)
				s.On("UpdateExternalEndpoint", strconv.Itoa(id), mock.MatchedBy(func(ee *v1.ExternalEndpoint) bool {
					return ee.Status != nil && ee.Status.Phase == v1.ExternalEndpointPhaseDELETED
				})).Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			gw := &gatewaymocks.MockGateway{}
			tt.setup(ms, gw)
			c := newTestExternalEndpointController(ms, gw)
			err := c.sync(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
			gw.AssertExpectations(t)
		})
	}
}

/* ---------- Create / Update ---------- */

func TestExternalEndpointController_Sync_CreateUpdate(t *testing.T) {
	id := 1
	tests := []struct {
		name    string
		in      *v1.ExternalEndpoint
		setup   func(*storagemocks.MockStorage, *gatewaymocks.MockGateway)
		wantErr bool
	}{
		{
			name: "sync success transitions Pending to Running",
			in:   ee(id, v1.ExternalEndpointPhasePENDING),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				g.On("SyncExternalEndpoint", mock.Anything).Return(nil)
				g.On("GetExternalEndpointServeUrl", mock.Anything).Return("http://serve-url", nil)
				s.On("UpdateExternalEndpoint", strconv.Itoa(id), mock.MatchedBy(func(ee *v1.ExternalEndpoint) bool {
					return ee.Status != nil &&
						ee.Status.Phase == v1.ExternalEndpointPhaseRUNNING &&
						ee.Status.ServiceURL == "http://serve-url"
				})).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "sync success with no prior status transitions to Running",
			in:   ee(id, ""),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				g.On("SyncExternalEndpoint", mock.Anything).Return(nil)
				g.On("GetExternalEndpointServeUrl", mock.Anything).Return("", nil)
				s.On("UpdateExternalEndpoint", strconv.Itoa(id), mock.MatchedBy(func(ee *v1.ExternalEndpoint) bool {
					return ee.Status != nil && ee.Status.Phase == v1.ExternalEndpointPhaseRUNNING
				})).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "sync failure transitions to Failed",
			in:   ee(id, v1.ExternalEndpointPhasePENDING),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				g.On("SyncExternalEndpoint", mock.Anything).Return(assert.AnError)
				s.On("UpdateExternalEndpoint", strconv.Itoa(id), mock.MatchedBy(func(ee *v1.ExternalEndpoint) bool {
					return ee.Status != nil && ee.Status.Phase == v1.ExternalEndpointPhaseFAILED
				})).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "already running skips status update",
			in:   ee(id, v1.ExternalEndpointPhaseRUNNING),
			setup: func(s *storagemocks.MockStorage, g *gatewaymocks.MockGateway) {
				g.On("SyncExternalEndpoint", mock.Anything).Return(nil)
			},
			wantErr: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &storagemocks.MockStorage{}
			gw := &gatewaymocks.MockGateway{}
			tt.setup(ms, gw)
			c := newTestExternalEndpointController(ms, gw)
			err := c.sync(tt.in)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			ms.AssertExpectations(t)
			gw.AssertExpectations(t)
		})
	}
}
