package controllers

import (
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/cluster"
	clustermocks "github.com/neutree-ai/neutree/internal/cluster/mocks"
	gatewaymocks "github.com/neutree-ai/neutree/internal/gateway/mocks"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
)

func newTestClusterController(s *storagemocks.MockStorage,
	r *clustermocks.MockClusterReconcile) *ClusterController {
	obsCollectConfigManager, _ := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		LocalCollectConfigPath: "tmp",
	})

	cluster.NewReconcile = func(cluster *v1.Cluster, acceleratorManager accelerator.Manager, s storage.Storage, metricsRemoteWriteURL string) (cluster.ClusterReconcile, error) {
		return r, nil
	}

	gw := &gatewaymocks.MockGateway{}
	gw.On("SyncCluster", mock.Anything, mock.Anything).Return(nil)
	gw.On("DeleteCluster", mock.Anything, mock.Anything).Return(nil)
	return &ClusterController{
		storage:                 s,
		defaultClusterVersion:   "v1",
		obsCollectConfigManager: obsCollectConfigManager,
		gw:                      gw,
	}
}

func TestClusterController_Sync_Delete(t *testing.T) {
	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Phase: v1.ClusterPhaseDeleted,
			},
		}
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "Deleted -> Deleted (storage delete success)",
			input: getTestCluster(),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteCluster", "1").Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Deleted -> Deleted (storage delete error)",
			input: getTestCluster(),
			mockSetup: func(s *storagemocks.MockStorage) {
				s.On("DeleteCluster", "1").Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storage := new(storagemocks.MockStorage)
			tt.mockSetup(storage)
			c := newTestClusterController(storage, nil)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestClusterController_Sync_PendingOrNoStatus(t *testing.T) {
	getTestCluster := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name: "test",
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
		}
	}

	getTestClusterWithDeletionTimestamp := func() *v1.Cluster {
		return &v1.Cluster{
			ID: 1,
			Metadata: &v1.Metadata{
				Name:              "test",
				DeletionTimestamp: time.Now().Format(time.RFC3339Nano),
			},
			Spec: &v1.ClusterSpec{
				ImageRegistry: "test",
				Type:          "ssh",
				Version:       "v2",
			},
			Status: &v1.ClusterStatus{
				Initialized: true,
			},
		}
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*storagemocks.MockStorage, *clustermocks.MockClusterReconcile)
		wantErr   bool
	}{
		{
			name:  "Pending/NoStatus -> Running (reconcile cluster success)",
			input: getTestCluster(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Failed (reconcile cluster failed)",
			input: getTestCluster(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name:  "Pending/NoStatus -> Deleted (reconcile delete cluster success)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("ReconcileDelete", mock.Anything, mock.Anything).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleted, obj.Status.Phase)
					assert.Equal(t, true, obj.Status.Initialized)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name:  "Pending/NoStatus -> Pending/NoStatus (delete cluster failed)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("ReconcileDelete", mock.Anything, mock.Anything).Return(assert.AnError)
			},
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			mockStorage := &storagemocks.MockStorage{}
			mockReconcile := &clustermocks.MockClusterReconcile{}
			tt.mockSetup(mockStorage, mockReconcile)

			c := newTestClusterController(mockStorage, mockReconcile)
			err := c.sync(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
			}
			mockStorage.AssertExpectations(t)
			mockReconcile.AssertExpectations(t)
		})
	}
}

func TestClusterController_Reconcile(t *testing.T) {
	tests := []struct {
		name      string
		input     interface{}
		mockSetup func(*storagemocks.MockStorage)
		wantErr   bool
	}{
		{
			name:  "success",
			input: &v1.Cluster{Metadata: &v1.Metadata{Name: "test"}},
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

			c := &ClusterController{storage: mockStorage, syncHandler: func(*v1.Cluster) error { return nil }}
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
