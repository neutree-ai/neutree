package cluster

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratormocks "github.com/neutree-ai/neutree/internal/accelerator/mocks"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	dashboardmocks "github.com/neutree-ai/neutree/internal/ray/dashboard/mocks"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

type fakeLegacyStaticUpgradeCleaner struct {
	deleteCalled bool
}

func (f *fakeLegacyStaticUpgradeCleaner) Reconcile(context.Context, *v1.Cluster) error {
	return nil
}

func (f *fakeLegacyStaticUpgradeCleaner) ReconcileDelete(context.Context, *v1.Cluster) error {
	f.deleteCalled = true
	return nil
}

func TestStaticRayReconcilerCleansLegacyRuntimeBeforeCreate(t *testing.T) {
	store := &storagemocks.MockStorage{}
	legacyCleaner := &fakeLegacyStaticUpgradeCleaner{}
	reconciler := &staticRayReconciler{
		storage: store,
		legacy:  legacyCleaner,
	}
	cluster := staticRayTestCluster("static-upgrade", "v1.0.2", "10.0.0.10")
	cluster.ID = 3
	cluster.Status = &v1.ClusterStatus{Initialized: true, Version: "v1.0.1"}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil).Once()
	store.On("UpdateCluster", "3", mock.MatchedBy(func(updated *v1.Cluster) bool {
		require.NotNil(t, updated.Status)
		assert.Equal(t, v1.ClusterPhaseUpgrading, updated.Status.Phase)

		return true
	})).Return(nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("CreateStaticNodeCluster", mock.MatchedBy(func(_ *v1.StaticNodeCluster) bool {
		assert.True(t, legacyCleaner.deleteCalled, "legacy cleanup must finish before creating StaticNodeCluster")
		return true
	})).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-upgrade is provisioning")
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerCreatesStaticNodeCluster(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-a", "v1.0.2", "10.0.0.10", "10.0.0.11")

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("CreateStaticNodeCluster", mock.MatchedBy(func(created *v1.StaticNodeCluster) bool {
		require.NotNil(t, created.Metadata)
		require.NotNil(t, created.Spec)
		assert.Equal(t, "static-a", created.Metadata.Name)
		assert.Equal(t, "default", created.Metadata.Workspace)
		assert.Equal(t, "v1.0.2", created.Spec.Version)
		assert.Equal(t, "registry.example.com/neutree", created.Spec.ImageRegistry)
		require.NotNil(t, created.Spec.UpgradeStrategy)
		assert.Equal(t, v1.ClusterUpgradeStrategyTypeRecreate, created.Spec.UpgradeStrategy.Type)
		require.Len(t, created.Spec.Nodes, 2)
		assert.Equal(t, "10.0.0.10", created.Spec.Nodes[0].Name)
		assert.Equal(t, v1.StaticNodeRoleHead, created.Spec.Nodes[0].Role)
		assert.Equal(t, "10.0.0.11", created.Spec.Nodes[1].Name)
		assert.Equal(t, v1.StaticNodeRoleWorker, created.Spec.Nodes[1].Role)

		return true
	})).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-a is provisioning")
	require.NotNil(t, cluster.Status)
	assert.Equal(t, "http://10.0.0.10:8265", cluster.Status.DashboardURL)
	assert.Equal(t, 2, cluster.Status.DesiredNodes)
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerRejectsInitializedHeadChange(t *testing.T) {
	reconciler := &staticRayReconciler{}
	cluster := staticRayTestCluster("static-head-change", "v1.0.2", "10.0.0.20")
	cluster.Status = &v1.ClusterStatus{
		Initialized:  true,
		Version:      "v1.0.2",
		DashboardURL: "http://10.0.0.10:8265",
	}

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "initialized static cluster head ip can not be changed")
}

func TestStaticRayReconcilerDeletePropagatesForceDelete(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{
			Name:      "static-force",
			Workspace: "default",
			Annotations: map[string]string{
				"neutree.ai/force-delete": "true",
			},
		},
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		{
			ID:       45,
			Metadata: &v1.Metadata{Name: "static-force", Workspace: "default"},
		},
	}, nil).Once()
	store.On("UpdateStaticNodeCluster", "45", mock.MatchedBy(func(updated *v1.StaticNodeCluster) bool {
		require.NotNil(t, updated.Metadata)
		assert.True(t, v1.IsForceDelete(updated.Metadata.Annotations))
		assert.NotEmpty(t, updated.Metadata.DeletionTimestamp)

		return true
	})).Return(nil).Once()

	err := reconciler.ReconcileDelete(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-force is deleting")
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerReturnsNotReadyWhenStaticNodeClusterProvisioning(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-updating", "v1.0.2", "10.0.0.10")
	cluster.Status = &v1.ClusterStatus{
		Initialized:      true,
		Version:          "v1.0.2",
		ObservedSpecHash: ComputeClusterSpecHash(cluster.Spec),
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		staticNodeClusterForTest(55, "static-updating", "v1.0.2", &v1.StaticNodeClusterStatus{
			Phase:        v1.StaticNodeClusterPhaseProvisioning,
			DesiredNodes: 1,
			ErrorMessage: "static node 10.0.0.10 phase=Reconciling",
		}),
	}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "55", mock.Anything).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-updating is not ready")
	assert.Contains(t, err.Error(), "static node 10.0.0.10 phase=Reconciling")
	require.NotNil(t, cluster.Status)
	assert.Equal(t, 1, cluster.Status.DesiredNodes)
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerReportsDesiredNodesFromDesiredSpec(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-scale-out", "v1.0.2", "10.0.0.10", "10.0.0.11", "10.0.0.12")
	cluster.Status = &v1.ClusterStatus{
		Initialized:      true,
		Version:          "v1.0.2",
		ObservedSpecHash: ComputeClusterSpecHash(cluster.Spec),
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		staticNodeClusterForTest(56, "static-scale-out", "v1.0.2", &v1.StaticNodeClusterStatus{
			Phase:        v1.StaticNodeClusterPhaseProvisioning,
			ReadyNodes:   1,
			DesiredNodes: 1,
		}),
	}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "56", mock.Anything).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "static node cluster static-scale-out is not ready")
	require.NotNil(t, cluster.Status)
	assert.Equal(t, 3, cluster.Status.DesiredNodes)
	assert.Equal(t, 1, cluster.Status.ReadyNodes)
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerWaitsWhenStaticNodeClusterStatusIsStale(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-scale-stale-status", "v1.0.2", "10.0.0.10", "10.0.0.11", "10.0.0.12")
	cluster.Status = &v1.ClusterStatus{
		Initialized:      true,
		Version:          "v1.0.2",
		ObservedSpecHash: ComputeClusterSpecHash(cluster.Spec),
	}
	current := staticNodeClusterForTest(59, "static-scale-stale-status", "v1.0.2", &v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseReady,
		Version:      "v1.0.2",
		ReadyNodes:   1,
		DesiredNodes: 1,
	})
	current.Spec.Nodes = []v1.StaticNodeClusterNodeSpec{
		{Name: "10.0.0.10", IP: "10.0.0.10", Role: v1.StaticNodeRoleHead, SSHAuth: &v1.Auth{SSHUser: "root", SSHPrivateKey: "key"}},
		{Name: "10.0.0.11", IP: "10.0.0.11", Role: v1.StaticNodeRoleWorker, SSHAuth: &v1.Auth{SSHUser: "root", SSHPrivateKey: "key"}},
		{Name: "10.0.0.12", IP: "10.0.0.12", Role: v1.StaticNodeRoleWorker, SSHAuth: &v1.Auth{SSHUser: "root", SSHPrivateKey: "key"}},
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{current}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "59", mock.Anything).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "status is applying desired spec")
	require.NotNil(t, cluster.Status)
	assert.Equal(t, 3, cluster.Status.DesiredNodes)
	assert.Equal(t, 1, cluster.Status.ReadyNodes)
	assert.Nil(t, cluster.Status.ResourceInfo)
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerWaitsWhenStaticNodeClusterReadyButSpecChanged(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-upgrade-ready-stale", "v1.0.3", "10.0.0.10")
	cluster.Status = &v1.ClusterStatus{
		Initialized:  true,
		Version:      "v1.0.2",
		DashboardURL: "http://10.0.0.10:8265",
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		staticNodeClusterForTest(57, "static-upgrade-ready-stale", "v1.0.2", &v1.StaticNodeClusterStatus{
			Phase:        v1.StaticNodeClusterPhaseReady,
			Version:      "v1.0.2",
			ReadyNodes:   1,
			DesiredNodes: 1,
		}),
	}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "57", mock.Anything).Return(nil).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is applying desired spec")
	require.NotNil(t, cluster.Status)
	assert.Equal(t, "v1.0.2", cluster.Status.Version)
	assert.NotEqual(t, ComputeClusterSpecHash(cluster.Spec), cluster.Status.ObservedSpecHash)
	store.AssertExpectations(t)
}

func TestStaticRayReconcilerCalculateResourcesFromRayDashboard(t *testing.T) {
	store := &storagemocks.MockStorage{}
	mockDashboard := &dashboardmocks.MockDashboardService{}
	mockAcceleratorManager := &acceleratormocks.MockManager{}
	reconciler := &staticRayReconciler{
		storage:            store,
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
				Labels: map[string]string{
					v1.NeutreeServingVersionLabel: "v1.0.2",
				},
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
	}, nil).Maybe()
	mockAcceleratorManager.On("GetAllParsers").Return(map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	}).Once()

	resources, err := reconciler.calculateResources(&v1.StaticNodeCluster{
		Metadata: &v1.Metadata{Name: "static-a", Workspace: "default"},
		Spec: &v1.StaticNodeClusterSpec{
			Version: "v1.0.2",
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{Name: "192.168.19.218", IP: "192.168.19.218", Role: v1.StaticNodeRoleHead},
			},
		},
	})

	require.NoError(t, err)
	require.NotNil(t, resources)
	require.Contains(t, resources.Allocatable.AcceleratorGroups, v1.AcceleratorTypeNVIDIAGPU)
	require.Contains(t, resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups, v1.AcceleratorProduct("NVIDIA_Tesla_T4"))
	assert.Equal(t, float64(2),
		resources.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups["NVIDIA_Tesla_T4"])
	require.Contains(t, resources.Available.AcceleratorGroups, v1.AcceleratorTypeNVIDIAGPU)
	require.Contains(t, resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups, v1.AcceleratorProduct("NVIDIA_Tesla_T4"))
	assert.Equal(t, float64(1),
		resources.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].ProductGroups["NVIDIA_Tesla_T4"])
	require.Contains(t, resources.NodeResources, "192.168.19.218")
	assert.Empty(t, resources.NodeResources["192.168.19.218"].Devices)

	mockDashboard.AssertExpectations(t)
	mockAcceleratorManager.AssertExpectations(t)
	store.AssertExpectations(t)
}

func connectedStaticNodeImageRegistry() v1.ImageRegistry {
	return v1.ImageRegistry{
		Metadata: &v1.Metadata{Name: "registry-a", Workspace: "default"},
		Spec: &v1.ImageRegistrySpec{
			URL:        "registry.example.com",
			Repository: "neutree",
		},
		Status: &v1.ImageRegistryStatus{Phase: v1.ImageRegistryPhaseCONNECTED},
	}
}

func staticRayTestCluster(name string, version string, headIP string, workerIPs ...string) *v1.Cluster {
	return &v1.Cluster{
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.ClusterSpec{
			ImageRegistry: "registry-a",
			Type:          v1.SSHClusterType,
			Version:       version,
			Config: &v1.ClusterConfig{
				SSHConfig: &v1.RaySSHProvisionClusterConfig{
					Provider: v1.Provider{
						HeadIP:    headIP,
						WorkerIPs: workerIPs,
					},
					Auth: v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
		},
	}
}

func staticNodeClusterForTest(
	id int,
	name string,
	version string,
	status *v1.StaticNodeClusterStatus,
) v1.StaticNodeCluster {
	return v1.StaticNodeCluster{
		ID:       id,
		Metadata: &v1.Metadata{Name: name, Workspace: "default"},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       version,
			ImageRegistry: "registry.example.com/neutree",
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name:    "10.0.0.10",
					IP:      "10.0.0.10",
					Role:    v1.StaticNodeRoleHead,
					SSHAuth: &v1.Auth{SSHUser: "root", SSHPrivateKey: "key"},
				},
			},
			UpgradeStrategy: v1.DefaultClusterUpgradeStrategy(),
		},
		Status: status,
	}
}

func TestStaticRayReconcilerDoesNotBlockWhenResourceCalculationFails(t *testing.T) {
	store := &storagemocks.MockStorage{}
	mockDashboard := &dashboardmocks.MockDashboardService{}
	reconciler := &staticRayReconciler{storage: store}
	cluster := staticRayTestCluster("static-a", "v1.0.2", "10.0.0.10")
	cluster.Status = &v1.ClusterStatus{
		Initialized:      true,
		Version:          "v1.0.2",
		ObservedSpecHash: ComputeClusterSpecHash(cluster.Spec),
	}

	prevFactory := dashboard.NewDashboardService
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboard
	}
	t.Cleanup(func() {
		dashboard.NewDashboardService = prevFactory
	})

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		staticNodeClusterForTest(58, "static-a", "v1.0.2", &v1.StaticNodeClusterStatus{
			Phase:        v1.StaticNodeClusterPhaseReady,
			Version:      "v1.0.2",
			ReadyNodes:   1,
			DesiredNodes: 1,
		}),
	}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "58", mock.Anything).Return(nil).Once()
	mockDashboard.On("ListNodes").Return(nil, errors.New("connection refused")).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.NotNil(t, cluster.Status)
	assert.Nil(t, cluster.Status.ResourceInfo)
	mockDashboard.AssertExpectations(t)
	store.AssertExpectations(t)
}
