package controllers

import (
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/cluster"
	clustermocks "github.com/neutree-ai/neutree/internal/cluster/mocks"
	gatewaymocks "github.com/neutree-ai/neutree/internal/gateway/mocks"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
)

func newTestClusterController(s *storagemocks.MockStorage,
	r *clustermocks.MockClusterReconcile) *ClusterController {
	obsCollectConfigManager, _ := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		LocalCollectConfigPath: "tmp",
	})

	gw := &gatewaymocks.MockGateway{}
	gw.On("SyncCluster", mock.Anything, mock.Anything).Return(nil)
	gw.On("DeleteCluster", mock.Anything, mock.Anything).Return(nil)
	s.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil).Maybe()
	return &ClusterController{
		storage:                 s,
		defaultClusterVersion:   "v1",
		obsCollectConfigManager: obsCollectConfigManager,
		gw:                      gw,
		newClusterReconcile: func(_ *v1.Cluster, _ accelerator.Manager, _ storage.Storage, _ string) (cluster.ClusterReconcile, error) {
			return r, nil
		},
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
				Version:       "v1.0.1",
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
				Version:       "v1.0.1",
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
				Version:       "v1.0.1",
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

func TestShouldUseStaticNodeClusterFlow(t *testing.T) {
	tests := []struct {
		name    string
		cluster *v1.Cluster
		want    bool
		wantErr string
	}{
		{
			name: "ssh v1.0.1 stays on legacy ray ssh reconcile",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1"},
			},
			want: false,
		},
		{
			name: "ssh v1.0.1 rc stays on legacy ray ssh reconcile",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.1-rc.1"},
			},
			want: false,
		},
		{
			name: "ssh v1.0.2 uses static node resources",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.0.2"},
			},
			want: true,
		},
		{
			name: "ssh v1.1.0 alpha uses static node resources",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.1.0-alpha.1"},
			},
			want: true,
		},
		{
			name: "kubernetes version never uses static node resources",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.1.0"},
			},
			want: false,
		},
		{
			name: "invalid version fails",
			cluster: &v1.Cluster{
				Spec: &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "custom"},
			},
			wantErr: "invalid cluster version",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got, err := shouldUseStaticNodeClusterFlow(tt.cluster)
			if tt.wantErr != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)

				return
			}

			require.NoError(t, err)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestClusterControllerSyncRejectsInvalidStaticClusterVersion(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	controller := &ClusterController{storage: mockStorage}
	input := &v1.Cluster{
		ID: 6,
		Metadata: &v1.Metadata{
			Name:      "static-invalid",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "custom",
		},
	}

	mockStorage.On("UpdateCluster", "6", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Equal(t, v1.ClusterPhaseInitializing, updated.Status.Phase)
		assert.Contains(t, updated.Status.ErrorMessage, "invalid cluster version")

		return true
	})).Return(nil)

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "invalid cluster version")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerLegacyToStaticNodeFlowCleansLegacyRuntimeBeforeCreate(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	cleanupCalled := false
	controller := &ClusterController{
		storage:               mockStorage,
		metricsRemoteWriteURL: "http://vmagent:8428/api/v1/write",
		cleanupLegacyStaticRuntime: func(_ *v1.Cluster) error {
			cleanupCalled = true

			return nil
		},
	}
	input := &v1.Cluster{
		ID: 7,
		Metadata: &v1.Metadata{
			Name:      "static-upgrade",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "registry-a",
			Type:          v1.SSHClusterType,
			Version:       "v1.0.2",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{HeadIP: "10.0.0.10"},
					Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
		},
		Status: &v1.ClusterStatus{
			Initialized: true,
			Version:     "v1.0.1",
		},
	}

	mockStorage.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
		{
			Metadata: &v1.Metadata{Name: "registry-a", Workspace: "default"},
			Spec: &v1.ImageRegistrySpec{
				URL:        "registry.example.com",
				Repository: "neutree",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
		},
	}, nil).Once()
	mockStorage.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil).Once()
	mockStorage.On("CreateStaticNodeCluster", mock.MatchedBy(func(_ *v1.StaticNodeCluster) bool {
		assert.True(t, cleanupCalled, "legacy cleanup must finish before creating StaticNodeCluster")

		return true
	})).Return(nil).Once()
	mockStorage.On("UpdateCluster", "7", mock.Anything).Return(nil)

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-upgrade is provisioning")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerStaticNodeToLegacyFlowDeletesStaticNodeClusterBeforeLegacyReconcile(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	controller := &ClusterController{
		storage: mockStorage,
		newClusterReconcile: func(_ *v1.Cluster, _ accelerator.Manager, _ storage.Storage, _ string) (cluster.ClusterReconcile, error) {
			require.Fail(t, "legacy cluster reconcile must wait until StaticNodeCluster cleanup finishes")

			return nil, nil
		},
	}
	input := &v1.Cluster{
		ID: 9,
		Metadata: &v1.Metadata{
			Name:      "static-rollback",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.0.1",
		},
		Status: &v1.ClusterStatus{
			Initialized: true,
			Version:     "v1.0.2",
		},
	}

	mockStorage.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		{
			ID: 44,
			Metadata: &v1.Metadata{
				Name:      "static-rollback",
				Workspace: "default",
			},
		},
	}, nil).Once()
	mockStorage.On("UpdateStaticNodeCluster", "44", mock.MatchedBy(func(updated *v1.StaticNodeCluster) bool {
		return updated.Metadata != nil && updated.Metadata.DeletionTimestamp != ""
	})).Return(nil).Once()
	mockStorage.On("UpdateCluster", "9", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Contains(t, updated.Status.ErrorMessage, "static node cluster static-rollback is deleting")

		return true
	})).Return(nil)

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-rollback is deleting")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerRejectsInitializedStaticNodeHeadChange(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	controller := &ClusterController{storage: mockStorage}
	input := &v1.Cluster{
		ID: 8,
		Metadata: &v1.Metadata{
			Name:      "static-head-change",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			Type:    v1.SSHClusterType,
			Version: "v1.0.2",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{HeadIP: "10.0.0.20"},
					Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
		},
		Status: &v1.ClusterStatus{
			Initialized:         true,
			Version:             "v1.0.2",
			NodeProvisionStatus: `{"10.0.0.10":{"status":"provisioned","is_head":true}}`,
		},
	}

	mockStorage.On("UpdateCluster", "8", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Contains(t, updated.Status.ErrorMessage, "initialized static cluster head ip can not be changed")

		return true
	})).Return(nil)

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialized static cluster head ip can not be changed")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerSyncCreatesStaticNodeClusterForNewSSHVersion(t *testing.T) {
	obsCollectConfigManager, _ := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		LocalCollectConfigPath: "tmp",
	})
	mockStorage := &storagemocks.MockStorage{}
	gw := &gatewaymocks.MockGateway{}
	controller := &ClusterController{
		storage:                 mockStorage,
		obsCollectConfigManager: obsCollectConfigManager,
		gw:                      gw,
		metricsRemoteWriteURL:   "http://vmagent:8428/api/v1/write",
		newClusterReconcile: func(_ *v1.Cluster, _ accelerator.Manager, _ storage.Storage, _ string) (cluster.ClusterReconcile, error) {
			require.Fail(t, "legacy cluster reconcile should not be used for new static node flow")

			return nil, nil
		},
	}
	input := &v1.Cluster{
		ID: 3,
		Metadata: &v1.Metadata{
			Name:      "static-a",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "registry-a",
			Type:          v1.SSHClusterType,
			Version:       "v1.0.2",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP:    "10.0.0.10",
						WorkerIPs: []string{"10.0.0.11"},
					},
					Auth: v1.Auth{
						SSHUser:       "root",
						SSHPrivateKey: "key",
					},
				},
			},
		},
	}

	mockStorage.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{
		{
			Metadata: &v1.Metadata{Name: "registry-a", Workspace: "default"},
			Spec: &v1.ImageRegistrySpec{
				URL:        "registry.example.com",
				Repository: "neutree",
			},
			Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
		},
	}, nil)
	mockStorage.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil)
	mockStorage.On("CreateStaticNodeCluster", mock.MatchedBy(func(created *v1.StaticNodeCluster) bool {
		require.NotNil(t, created.Metadata)
		require.NotNil(t, created.Spec)
		assert.Equal(t, "static-a", created.Metadata.Name)
		assert.Equal(t, "default", created.Metadata.Workspace)
		assert.Equal(t, "v1.0.2", created.Spec.Version)
		assert.Equal(t, "registry.example.com/neutree", created.Spec.ImageRegistry)
		assert.Equal(t, "http://vmagent:8428/api/v1/write", created.Spec.MetricsRemoteWriteURL)
		require.NotNil(t, created.Spec.UpgradeStrategy)
		assert.Equal(t, v1.ClusterUpgradeStrategyTypeRecreate, created.Spec.UpgradeStrategy.Type)
		require.Len(t, created.Spec.Nodes, 2)
		assert.Equal(t, "10.0.0.10", created.Spec.Nodes[0].Name)
		assert.Equal(t, v1.StaticNodeRoleHead, created.Spec.Nodes[0].Role)
		assert.Equal(t, "10.0.0.11", created.Spec.Nodes[1].Name)
		assert.Equal(t, v1.StaticNodeRoleWorker, created.Spec.Nodes[1].Role)

		return true
	})).Return(nil)
	mockStorage.On("UpdateCluster", "3", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Equal(t, v1.ClusterPhaseInitializing, updated.Status.Phase)
		assert.Equal(t, "http://10.0.0.10:8265", updated.Status.DashboardURL)
		assert.Equal(t, 2, updated.Status.DesiredNodes)

		return true
	})).Return(nil)

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-a is provisioning")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerSyncMapsStaticNodeClusterProvisioningToUpdating(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	controller := &ClusterController{
		storage:               mockStorage,
		metricsRemoteWriteURL: "http://vmagent:8428/api/v1/write",
	}
	input := &v1.Cluster{
		ID: 10,
		Metadata: &v1.Metadata{
			Name:      "static-updating",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "registry-a",
			Type:          v1.SSHClusterType,
			Version:       "v1.0.2",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{HeadIP: "10.0.0.10"},
					Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
		},
	}
	input.Status = &v1.ClusterStatus{
		Initialized:      true,
		Version:          "v1.0.2",
		ObservedSpecHash: cluster.ComputeClusterSpecHash(input.Spec),
	}

	mockStorage.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		{
			ID: 55,
			Metadata: &v1.Metadata{
				Name:      "static-updating",
				Workspace: "default",
			},
			Spec: &v1.StaticNodeClusterSpec{
				Version:       "v1.0.2",
				ImageRegistry: "registry.example.com/neutree",
				Nodes: []v1.StaticNodeClusterNodeSpec{
					{Name: "10.0.0.10", IP: "10.0.0.10", Role: v1.StaticNodeRoleHead},
				},
			},
			Status: &v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseProvisioning,
				DesiredNodes: 1,
				ErrorMessage: "static node 10.0.0.10 phase=Reconciling",
			},
		},
	}, nil).Once()
	mockStorage.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedImageRegistry()}, nil).Once()
	mockStorage.On("UpdateStaticNodeCluster", "55", mock.Anything).Return(nil).Once()
	mockStorage.On("UpdateCluster", "10", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Equal(t, v1.ClusterPhaseUpdating, updated.Status.Phase)
		assert.Contains(t, updated.Status.ErrorMessage, "static node 10.0.0.10 phase=Reconciling")

		return true
	})).Return(nil).Once()

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-updating is not ready")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerSyncMapsStaticNodeClusterWarmingToUpgrading(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	controller := &ClusterController{
		storage:               mockStorage,
		metricsRemoteWriteURL: "http://vmagent:8428/api/v1/write",
	}
	input := &v1.Cluster{
		ID: 11,
		Metadata: &v1.Metadata{
			Name:      "static-upgrading",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "registry-a",
			Type:          v1.SSHClusterType,
			Version:       "v1.0.2",
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{HeadIP: "10.0.0.10"},
					Auth:     v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
		},
		Status: &v1.ClusterStatus{
			Initialized:  true,
			Version:      "v1.0.1",
			DashboardURL: "http://10.0.0.10:8265",
		},
	}

	mockStorage.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		{
			ID: 56,
			Metadata: &v1.Metadata{
				Name:      "static-upgrading",
				Workspace: "default",
			},
			Spec: &v1.StaticNodeClusterSpec{
				Version:       "v1.0.1",
				ImageRegistry: "registry.example.com/neutree",
				Nodes: []v1.StaticNodeClusterNodeSpec{
					{Name: "10.0.0.10", IP: "10.0.0.10", Role: v1.StaticNodeRoleHead},
				},
			},
			Status: &v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseUpgrading,
				Version:      "v1.0.1",
				ErrorMessage: "Warming",
			},
		},
	}, nil).Once()
	mockStorage.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedImageRegistry()}, nil).Once()
	mockStorage.On("UpdateStaticNodeCluster", "56", mock.Anything).Return(nil).Once()
	mockStorage.On("UpdateCluster", "11", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Equal(t, v1.ClusterPhaseUpgrading, updated.Status.Phase)

		return true
	})).Return(nil).Once()

	err := controller.sync(input)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-upgrading is not ready")
	mockStorage.AssertExpectations(t)
}

func TestClusterControllerCalculateStaticNodeClusterResourcesEnrichesFromStaticNodeDevices(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockDashboard := &dashboardmocks.MockDashboardService{}
	mockAcceleratorManager := &acceleratormocks.MockManager{}
	controller := &ClusterController{
		storage:            mockStorage,
		acceleratorManager: mockAcceleratorManager,
	}

	prevFactory := dashboard.NewDashboardService
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboard
	}
	t.Cleanup(func() {
		dashboard.NewDashboardService = prevFactory
	})

	mockDashboard.On("ListNodes").Return([]v1.NodeSummary{
		{
			IP: "192.168.19.218",
			Raylet: v1.Raylet{
				State: v1.AliveNodeState,
				Resources: map[string]float64{
					"CPU":             32,
					"GPU":             2,
					"memory":          64 * resourceview.BytesPerGiB,
					"NVIDIA_Tesla_T4": 2,
				},
				CoreWorkersStats: []v1.CoreWorkerStats{
					{
						UsedResources: map[string]v1.RayResourceAllocations{
							"GPU": {
								ResourceSlots: []v1.RayResourceSlot{{Allocation: 1}},
							},
							"NVIDIA_Tesla_T4": {
								ResourceSlots: []v1.RayResourceSlot{{Allocation: 1}},
							},
						},
					},
				},
			},
		},
	}, nil).Once()
	mockAcceleratorManager.On("GetAllParsers").Return(map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	}).Once()

	mockStorage.On("GenericQuery", storage.STATIC_NODE_TABLE, "*", mock.Anything, mock.Anything).
		Run(func(args mock.Arguments) {
			nodes, ok := args.Get(3).(*[]v1.StaticNode)
			require.True(t, ok)
			*nodes = []v1.StaticNode{
				{
					Metadata: &v1.Metadata{
						Name:      "192.168.19.218",
						Workspace: "default",
					},
					Spec: &v1.StaticNodeSpec{
						Cluster: "static-a",
						IP:      "192.168.19.218",
						Role:    v1.StaticNodeRoleHead,
					},
					Status: &v1.StaticNodeStatus{
						Accelerator: &v1.StaticNodeAcceleratorStatus{
							Type:         string(v1.AcceleratorTypeNVIDIAGPU),
							ProductModel: "Tesla T4",
							Devices: []v1.StaticNodeAcceleratorDeviceStatus{
								{
									UUID:         "GPU-0",
									ProductModel: "Tesla T4",
									MemoryMiB:    15360,
									Healthy:      true,
								},
								{
									UUID:         "GPU-1",
									ProductModel: "Tesla T4",
									MemoryMiB:    15360,
									Healthy:      true,
								},
							},
						},
						Allocations: []v1.StaticNodeAllocationStatus{
							{
								Endpoint:   "demo-ep",
								InstanceID: "actor-a",
								Devices: []v1.DeviceAllocation{
									{
										UUID:      "GPU-0",
										Product:   "Tesla T4",
										MemoryMiB: 15360,
										CoreUnits: 100,
										NodeID:    "192.168.19.218",
									},
								},
							},
						},
					},
				},
			}
		}).
		Return(nil).
		Once()

	resources, err := controller.calculateStaticNodeClusterResources(&v1.StaticNodeCluster{
		Metadata: &v1.Metadata{Name: "static-a", Workspace: "default"},
		Spec: &v1.StaticNodeClusterSpec{
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{Name: "192.168.19.218", IP: "192.168.19.218", Role: v1.StaticNodeRoleHead},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, resources)
	require.NotNil(t, resources.AcceleratorMetadata)
	require.Contains(t, resources.AcceleratorMetadata, v1.AcceleratorTypeNVIDIAGPU)
	require.Contains(t, resources.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products, v1.AcceleratorProduct("NVIDIA_Tesla_T4"))
	assert.Equal(t, float64(15360),
		resources.AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_Tesla_T4"].MemoryTotalMiB)
	require.Contains(t, resources.Allocatable.AcceleratorGroups, v1.AcceleratorTypeNVIDIAGPU)
	require.Contains(t, resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Products, v1.AcceleratorProduct("NVIDIA_Tesla_T4"))
	assert.Equal(t, float64(2),
		resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_Tesla_T4"].Quantity)
	require.Contains(t, resources.Available.AcceleratorGroups, v1.AcceleratorTypeNVIDIAGPU)
	require.Contains(t, resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Products, v1.AcceleratorProduct("NVIDIA_Tesla_T4"))
	assert.Equal(t, float64(1),
		resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_Tesla_T4"].Quantity)
	require.Contains(t, resources.NodeResources, "192.168.19.218")
	require.Len(t, resources.NodeResources["192.168.19.218"].Devices, 2)
	assert.Equal(t, "NVIDIA_Tesla_T4", resources.NodeResources["192.168.19.218"].Devices[0].Product)
	assert.Equal(t, int64(0), resources.NodeResources["192.168.19.218"].Devices[0].Available.MemoryMiB)
	assert.Equal(t, int64(15360), resources.NodeResources["192.168.19.218"].Devices[1].Available.MemoryMiB)

	mockDashboard.AssertExpectations(t)
	mockAcceleratorManager.AssertExpectations(t)
	mockStorage.AssertExpectations(t)
}

func connectedImageRegistry() v1.ImageRegistry {
	return v1.ImageRegistry{
		Metadata: &v1.Metadata{Name: "registry-a", Workspace: "default"},
		Spec: &v1.ImageRegistrySpec{
			URL:        "registry.example.com",
			Repository: "neutree",
		},
		Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
	}
}

func TestClusterController_UpdateClusterStatus(t *testing.T) {
	specV2 := &v1.ClusterSpec{
		ImageRegistry: "test",
		Type:          "ssh",
		Version:       "v1.0.1",
	}
	specV2Hash := cluster.ComputeClusterSpecHash(specV2)

	specV3 := &v1.ClusterSpec{
		ImageRegistry: "test-v2",
		Type:          "ssh",
		Version:       "v1.0.1",
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
