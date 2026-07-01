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
	"github.com/neutree-ai/neutree/pkg/storage"
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

func TestStaticNodeClusterBackedRayReconcilerCleansLegacyRuntimeBeforeCreate(t *testing.T) {
	store := &storagemocks.MockStorage{}
	legacyCleaner := &fakeLegacyStaticUpgradeCleaner{}
	reconciler := &staticNodeClusterBackedRayReconciler{
		storage: store,
		legacy:  legacyCleaner,
	}
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "static-upgrade", Workspace: "default"},
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
		Status: &v1.ClusterStatus{Initialized: true, Version: "v1.0.1"},
	}

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{}, nil).Once()
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

func TestStaticNodeClusterBackedRayReconcilerDeletePropagatesForceDelete(t *testing.T) {
	store := &storagemocks.MockStorage{}
	reconciler := &staticNodeClusterBackedRayReconciler{storage: store}
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

func TestStaticNodeClusterBackedRayReconcilerCalculateResourcesEnrichesFromStaticNodeDevices(t *testing.T) {
	store := &storagemocks.MockStorage{}
	mockDashboard := &dashboardmocks.MockDashboardService{}
	mockAcceleratorManager := &acceleratormocks.MockManager{}
	reconciler := &staticNodeClusterBackedRayReconciler{
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
	}, nil).Twice()
	mockAcceleratorManager.On("GetAllParsers").Return(map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	}).Once()

	store.On("ListStaticNode", mock.MatchedBy(func(option storage.ListOption) bool {
		return len(option.Filters) == 2
	})).
		Return([]v1.StaticNode{
			{
				Metadata: &v1.Metadata{Name: "192.168.19.218", Workspace: "default"},
				Spec: &v1.StaticNodeSpec{
					Cluster: "static-a",
					IP:      "192.168.19.218",
					Role:    v1.StaticNodeRoleHead,
				},
				Status: &v1.StaticNodeStatus{
					Accelerator: &v1.StaticNodeAcceleratorStatus{
						Type: string(v1.AcceleratorTypeNVIDIAGPU),
						Devices: []v1.StaticNodeAcceleratorDeviceStatus{
							{UUID: "GPU-0", ProductModel: "Tesla T4", MemoryMiB: 15360, Healthy: true},
							{UUID: "GPU-1", ProductModel: "Tesla T4", MemoryMiB: 15360, Healthy: true},
						},
					},
				},
			},
		}, nil).
		Once()

	resources, err := reconciler.calculateStaticNodeClusterResources(&v1.StaticNodeCluster{
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
	assert.Nil(t, resources.NodeResources["192.168.19.218"].Devices[0].Available)
	assert.Nil(t, resources.NodeResources["192.168.19.218"].Devices[1].Available)

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

func TestStaticNodeClusterBackedRayReconcilerMarksApplyingPhaseForGenericClusterPhase(t *testing.T) {
	reconciler := &staticNodeClusterBackedRayReconciler{}
	cluster := &v1.Cluster{Status: &v1.ClusterStatus{Initialized: true}}

	reconciler.markStaticNodeClusterApplying(cluster, &v1.StaticNodeClusterStatus{
		Phase: v1.StaticNodeClusterPhaseProvisioning,
	}, false)

	assert.Equal(t, v1.ClusterPhaseUpdating, DetermineClusterPhase(false, cluster))

	reconciler.markStaticNodeClusterApplying(cluster, &v1.StaticNodeClusterStatus{
		Phase: v1.StaticNodeClusterPhaseUpgrading,
	}, false)

	assert.Equal(t, v1.ClusterPhaseUpgrading, DetermineClusterPhase(false, cluster))

	reconciler.markStaticNodeClusterApplying(cluster, &v1.StaticNodeClusterStatus{
		Phase: v1.StaticNodeClusterPhaseFailed,
	}, false)

	assert.Equal(t, v1.ClusterPhaseFailed, DetermineClusterPhase(false, cluster))
}

func TestStaticNodeClusterBackedRayReconcilerWrapsDashboardVerificationAsApplying(t *testing.T) {
	store := &storagemocks.MockStorage{}
	mockDashboard := &dashboardmocks.MockDashboardService{}
	reconciler := &staticNodeClusterBackedRayReconciler{storage: store}
	cluster := &v1.Cluster{
		Metadata: &v1.Metadata{Name: "static-a", Workspace: "default"},
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
			Initialized:      true,
			Version:          "v1.0.2",
			ObservedSpecHash: ComputeClusterSpecHash(&v1.ClusterSpec{ImageRegistry: "registry-a", Type: v1.SSHClusterType, Version: "v1.0.2"}),
		},
	}

	prevFactory := dashboard.NewDashboardService
	dashboard.NewDashboardService = func(_ string) dashboard.DashboardService {
		return mockDashboard
	}
	t.Cleanup(func() {
		dashboard.NewDashboardService = prevFactory
	})

	store.On("ListStaticNodeCluster", mock.Anything).Return([]v1.StaticNodeCluster{
		{
			ID:       58,
			Metadata: &v1.Metadata{Name: "static-a", Workspace: "default"},
			Spec: &v1.StaticNodeClusterSpec{
				Version:       "v1.0.2",
				ImageRegistry: "registry.example.com/neutree",
				Nodes: []v1.StaticNodeClusterNodeSpec{
					{Name: "10.0.0.10", IP: "10.0.0.10", Role: v1.StaticNodeRoleHead, SSHAuth: &v1.Auth{SSHUser: "root", SSHPrivateKey: "key"}},
				},
				UpgradeStrategy: v1.DefaultClusterUpgradeStrategy(),
			},
			Status: &v1.StaticNodeClusterStatus{
				Phase:        v1.StaticNodeClusterPhaseReady,
				Version:      "v1.0.2",
				ReadyNodes:   1,
				DesiredNodes: 1,
			},
		},
	}, nil).Once()
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{connectedStaticNodeImageRegistry()}, nil).Once()
	store.On("UpdateStaticNodeCluster", "58", mock.Anything).Return(nil).Once()
	mockDashboard.On("ListNodes").Return(nil, errors.New("connection refused")).Once()

	err := reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "Ray verification failed")
	assert.Equal(t, v1.ClusterPhaseUpdating, DetermineClusterPhase(false, cluster))
	mockDashboard.AssertExpectations(t)
	store.AssertExpectations(t)
}
