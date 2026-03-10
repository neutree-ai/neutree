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
			name:  "Pending/NoStatus -> Running (reconcile success)",
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
			name:  "Pending/NoStatus -> Initializing (reconcile failed, not initialized)",
			input: getTestCluster(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, obj.Status.Phase)
					assert.NotEmpty(t, obj.Status.ErrorMessage)
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
			name:  "Pending/NoStatus -> Deleting (delete cluster failed, non-force)",
			input: getTestClusterWithDeletionTimestamp(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("ReconcileDelete", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleting, obj.Status.Phase)
					assert.NotEmpty(t, obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Force delete -> Deleted even when reconcile fails",
			input: func() *v1.Cluster {
				c := getTestClusterWithDeletionTimestamp()
				c.Metadata.Annotations = map[string]string{
					"neutree.ai/force-delete": "true",
				}
				return c
			}(),
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("ReconcileDelete", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseDeleted, obj.Status.Phase)
				}).Return(nil)
			},
			wantErr: false,
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

func TestClusterController_UpdateClusterStatus(t *testing.T) {
	specV2 := &v1.ClusterSpec{
		ImageRegistry: "test",
		Type:          "ssh",
		Version:       "v2",
	}
	specV2Hash := cluster.ComputeClusterSpecHash(specV2)

	specV3 := &v1.ClusterSpec{
		ImageRegistry: "test",
		Type:          "ssh",
		Version:       "v3",
	}

	tests := []struct {
		name      string
		input     *v1.Cluster
		mockSetup func(*storagemocks.MockStorage, *clustermocks.MockClusterReconcile)
		wantErr   bool
	}{
		{
			name: "Running: reconcile succeeds -> Running with ObservedSpecHash",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status: &v1.ClusterStatus{
					Phase:            v1.ClusterPhaseRunning,
					Initialized:      true,
					ObservedSpecHash: specV2Hash,
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, specV2Hash, obj.Status.ObservedSpecHash)
					assert.Empty(t, obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Updating: reconcile fails with spec change -> Updating",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV3,
				Status: &v1.ClusterStatus{
					Phase:            v1.ClusterPhaseRunning,
					Initialized:      true,
					ObservedSpecHash: specV2Hash,
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseUpdating, obj.Status.Phase)
					// Hash preserved from existing status
					assert.Equal(t, specV2Hash, obj.Status.ObservedSpecHash)
					assert.NotEmpty(t, obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Initializing: reconcile fails, not initialized -> Initializing",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status:   &v1.ClusterStatus{},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseInitializing, obj.Status.Phase)
					assert.NotEmpty(t, obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Failed: reconcile fails, initialized, spec unchanged -> Failed",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status: &v1.ClusterStatus{
					Phase:            v1.ClusterPhaseRunning,
					Initialized:      true,
					ObservedSpecHash: specV2Hash,
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(assert.AnError)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseFailed, obj.Status.Phase)
					assert.NotEmpty(t, obj.Status.ErrorMessage)
				}).Return(nil)
			},
			wantErr: true,
		},
		{
			name: "Reconcile sets status fields in-memory -> preserved in storage",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status: &v1.ClusterStatus{
					Phase:            v1.ClusterPhaseRunning,
					Initialized:      true,
					ObservedSpecHash: specV2Hash,
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				accelType := "nvidia_gpu"
				o.On("Reconcile", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
					c := args.Get(1).(*v1.Cluster)
					c.Status.ReadyNodes = 3
					c.Status.DesiredNodes = 3
					c.Status.Version = "v1.0"
					c.Status.RayVersion = "2.53.0"
					c.Status.DashboardURL = "http://head:8265"
					c.Status.AcceleratorType = &accelType
				}).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, 3, obj.Status.ReadyNodes)
					assert.Equal(t, 3, obj.Status.DesiredNodes)
					assert.Equal(t, "v1.0", obj.Status.Version)
					assert.Equal(t, "2.53.0", obj.Status.RayVersion)
					assert.Equal(t, "http://head:8265", obj.Status.DashboardURL)
					assert.NotNil(t, obj.Status.AcceleratorType)
					assert.Equal(t, "nvidia_gpu", *obj.Status.AcceleratorType)
					assert.Equal(t, specV2Hash, obj.Status.ObservedSpecHash)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Backward compat: empty hash, reconcile succeeds -> Running with hash set",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status: &v1.ClusterStatus{
					Phase:            v1.ClusterPhaseRunning,
					Initialized:      true,
					ObservedSpecHash: "",
				},
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, specV2Hash, obj.Status.ObservedSpecHash)
				}).Return(nil)
			},
			wantErr: false,
		},
		{
			name: "Nil initial status, reconcile succeeds -> Running",
			input: &v1.Cluster{
				ID:       1,
				Metadata: &v1.Metadata{Name: "test"},
				Spec:     specV2,
				Status:   nil,
			},
			mockSetup: func(s *storagemocks.MockStorage, o *clustermocks.MockClusterReconcile) {
				o.On("Reconcile", mock.Anything, mock.Anything).Return(nil)
				s.On("UpdateCluster", "1", mock.Anything).Run(func(args mock.Arguments) {
					obj := args.Get(1).(*v1.Cluster)
					assert.Equal(t, v1.ClusterPhaseRunning, obj.Status.Phase)
					assert.Equal(t, specV2Hash, obj.Status.ObservedSpecHash)
				}).Return(nil)
			},
			wantErr: false,
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

func TestComputeClusterSpecHash(t *testing.T) {
	t.Run("same spec produces same hash", func(t *testing.T) {
		spec := &v1.ClusterSpec{Type: "ssh", Version: "v1", ImageRegistry: "test"}
		hash1 := cluster.ComputeClusterSpecHash(spec)
		hash2 := cluster.ComputeClusterSpecHash(spec)
		assert.Equal(t, hash1, hash2)
		assert.NotEmpty(t, hash1)
	})

	t.Run("different spec produces different hash", func(t *testing.T) {
		spec1 := &v1.ClusterSpec{Type: "ssh", Version: "v1", ImageRegistry: "test"}
		spec2 := &v1.ClusterSpec{Type: "ssh", Version: "v2", ImageRegistry: "test"}
		assert.NotEqual(t, cluster.ComputeClusterSpecHash(spec1), cluster.ComputeClusterSpecHash(spec2))
	})

	t.Run("credential change does not affect hash", func(t *testing.T) {
		spec1 := &v1.ClusterSpec{
			Type:    "ssh",
			Version: "v1",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Auth: v1.Auth{SSHPrivateKey: "key1"},
				},
			},
		}
		spec2 := &v1.ClusterSpec{
			Type:    "ssh",
			Version: "v1",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Auth: v1.Auth{SSHPrivateKey: "key2"},
				},
			},
		}
		assert.Equal(t, cluster.ComputeClusterSpecHash(spec1), cluster.ComputeClusterSpecHash(spec2))
	})

	t.Run("kubeconfig change does not affect hash", func(t *testing.T) {
		spec1 := &v1.ClusterSpec{
			Type:    "kubernetes",
			Version: "v1",
			Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: "config1"},
			},
		}
		spec2 := &v1.ClusterSpec{
			Type:    "kubernetes",
			Version: "v1",
			Config: &v1.ClusterConfig{
				KubernetesConfig: &v1.KubernetesClusterConfig{Kubeconfig: "config2"},
			},
		}
		assert.Equal(t, cluster.ComputeClusterSpecHash(spec1), cluster.ComputeClusterSpecHash(spec2))
	})
}
