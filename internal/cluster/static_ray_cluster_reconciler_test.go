package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestStaticRayClusterReconcilerCreatesStaticNodeCluster(t *testing.T) {
	store := &fakeStaticRayClusterStore{
		imageRegistries: []v1.ImageRegistry{connectedImageRegistry("registry.example.com", "neutree")},
	}
	cluster := testStaticRayCluster()
	reconciler, err := newStaticRayClusterReconcile(store, nil, "http://vm:8480/insert/0/prometheus/")
	require.NoError(t, err)

	err = reconciler.Reconcile(context.Background(), cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "is provisioning")
	require.Len(t, store.created, 1)

	created := store.created[0]
	require.NotNil(t, created.Metadata)
	require.NotNil(t, created.Spec)
	assert.Equal(t, "static-a", created.Metadata.Name)
	assert.Equal(t, "default", created.Metadata.Workspace)
	assert.Equal(t, "registry.example.com/neutree", created.Spec.ImageRegistry)
	assert.Equal(t, "v1.2.0", created.Spec.Version)
	assert.Equal(t, "http://vm:8480/insert/0/prometheus/", created.Spec.MetricsRemoteWriteURL)
	assert.Equal(t, "10.0.0.10", created.Spec.Head.NodeName)
	require.Len(t, created.Spec.Nodes, 2)
	assert.Equal(t, "10.0.0.10", created.Spec.Nodes[0].Name)
	assert.Equal(t, "10.0.0.10", created.Spec.Nodes[0].IP)
	assert.Equal(t, v1.StaticNodeRoleHead, created.Spec.Nodes[0].Role)
	assert.Equal(t, "10.0.0.11", created.Spec.Nodes[1].Name)
	assert.Equal(t, "10.0.0.11", created.Spec.Nodes[1].IP)
	assert.Equal(t, v1.StaticNodeRoleWorker, created.Spec.Nodes[1].Role)
	require.NotNil(t, created.Spec.Nodes[0].SSHAuth)
	assert.Equal(t, "root", created.Spec.Nodes[0].SSHAuth.SSHUser)
	assert.Equal(t, "key", created.Spec.Nodes[0].SSHAuth.SSHPrivateKey)
	assert.Equal(t, "http://10.0.0.10:8265", cluster.Status.DashboardURL)
	assert.Equal(t, 2, cluster.Status.DesiredNodes)
}

func TestStaticRayClusterReconcilerCopiesReadyStatus(t *testing.T) {
	store := &fakeStaticRayClusterStore{
		imageRegistries: []v1.ImageRegistry{connectedImageRegistry("registry.example.com", "neutree")},
		staticClusters: []v1.StaticNodeCluster{
			{
				ID: 7,
				Metadata: &v1.Metadata{
					Name:      "static-a",
					Workspace: "default",
				},
				Status: &v1.StaticNodeClusterStatus{
					Phase:        v1.StaticNodeClusterPhaseReady,
					DesiredNodes: 2,
					ReadyNodes:   2,
					HeadReady:    true,
					WarmReady:    true,
					MetricsReady: true,
				},
			},
		},
	}
	cluster := testStaticRayCluster()
	reconciler, err := newStaticRayClusterReconcile(store, nil, "http://vm:8480/insert/0/prometheus/")
	require.NoError(t, err)
	reconciler.newDashboardService = func(dashboardURL string) dashboard.DashboardService {
		assert.Equal(t, "http://10.0.0.10:8265", dashboardURL)

		return &fakeStaticRayDashboardService{
			nodes: []v1.NodeSummary{
				{
					IP: "10.0.0.10",
					Raylet: v1.Raylet{
						State: v1.AliveNodeState,
						Resources: map[string]float64{
							"CPU":    8,
							"memory": 16 * 1024 * 1024 * 1024,
						},
						CoreWorkersStats: []v1.CoreWorkerStats{
							{
								UsedResources: map[string]v1.RayResourceAllocations{
									"CPU": {
										ResourceSlots: []v1.RayResourceSlot{{Allocation: 4}},
									},
									"memory": {
										ResourceSlots: []v1.RayResourceSlot{{Allocation: 8 * 1024 * 1024 * 1024}},
									},
								},
							},
						},
					},
				},
			},
		}
	}

	err = reconciler.Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.Len(t, store.updated, 1)
	assert.Equal(t, 7, store.updated[0].ID)
	require.NotNil(t, cluster.Status)
	assert.True(t, cluster.Status.Initialized)
	assert.Equal(t, 2, cluster.Status.DesiredNodes)
	assert.Equal(t, 2, cluster.Status.ReadyNodes)
	assert.Equal(t, "v1.2.0", cluster.Status.Version)
	assert.Equal(t, "http://10.0.0.10:8265", cluster.Status.DashboardURL)
	require.NotNil(t, cluster.Status.ResourceInfo)
	assert.Equal(t, 8.0, cluster.Status.ResourceInfo.Allocatable.CPU)
	assert.Equal(t, 16.0, cluster.Status.ResourceInfo.Allocatable.Memory)
	assert.Equal(t, 4.0, cluster.Status.ResourceInfo.Available.CPU)
	assert.Equal(t, 8.0, cluster.Status.ResourceInfo.Available.Memory)
	require.Contains(t, cluster.Status.ResourceInfo.NodeResources, "10.0.0.10")
}

func TestStaticRayClusterReconcilerDeleteSoftDeletesStaticNodeCluster(t *testing.T) {
	store := &fakeStaticRayClusterStore{
		staticClusters: []v1.StaticNodeCluster{
			{
				ID: 9,
				Metadata: &v1.Metadata{
					Name:      "static-a",
					Workspace: "default",
				},
			},
		},
	}
	reconciler, err := newStaticRayClusterReconcile(store, nil, "")
	require.NoError(t, err)

	err = reconciler.ReconcileDelete(context.Background(), testStaticRayCluster())

	require.NoError(t, err)
	require.Len(t, store.updated, 1)
	assert.Equal(t, []string{"9"}, store.updatedIDs)
	require.NotNil(t, store.updated[0].Metadata)
	assert.NotEmpty(t, store.updated[0].Metadata.DeletionTimestamp)
	assert.Empty(t, store.deletedIDs)
}

type fakeStaticRayClusterStore struct {
	imageRegistries []v1.ImageRegistry
	staticClusters  []v1.StaticNodeCluster
	created         []*v1.StaticNodeCluster
	updated         []*v1.StaticNodeCluster
	updatedIDs      []string
	deletedIDs      []string
}

func (f *fakeStaticRayClusterStore) ListImageRegistry(_ storage.ListOption) ([]v1.ImageRegistry, error) {
	return f.imageRegistries, nil
}

func (f *fakeStaticRayClusterStore) ListStaticNodeCluster(_ storage.ListOption) ([]v1.StaticNodeCluster, error) {
	return f.staticClusters, nil
}

func (f *fakeStaticRayClusterStore) CreateStaticNodeCluster(data *v1.StaticNodeCluster) error {
	f.created = append(f.created, data)

	return nil
}

func (f *fakeStaticRayClusterStore) UpdateStaticNodeCluster(id string, data *v1.StaticNodeCluster) error {
	f.updatedIDs = append(f.updatedIDs, id)
	f.updated = append(f.updated, data)

	return nil
}

func (f *fakeStaticRayClusterStore) DeleteStaticNodeCluster(id string) error {
	f.deletedIDs = append(f.deletedIDs, id)

	return nil
}

func testStaticRayCluster() *v1.Cluster {
	acceleratorType := v1.AcceleratorTypeNVIDIAGPU.String()

	return &v1.Cluster{
		ID: 3,
		Metadata: &v1.Metadata{
			Name:      "static-a",
			Workspace: "default",
		},
		Spec: &v1.ClusterSpec{
			Type:          v1.SSHClusterType,
			ImageRegistry: "registry-a",
			Version:       "v1.2.0",
			Config: &v1.ClusterConfig{
				AcceleratorType: &acceleratorType,
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
}

func connectedImageRegistry(url string, repository string) v1.ImageRegistry {
	return v1.ImageRegistry{
		Metadata: &v1.Metadata{
			Name:      "registry-a",
			Workspace: "default",
		},
		Spec: &v1.ImageRegistrySpec{
			URL:        url,
			Repository: repository,
		},
		Status: &v1.ImageRegistryStatus{
			Phase: v1.ImageRegistryPhaseCONNECTED,
		},
	}
}

type fakeStaticRayDashboardService struct {
	nodes []v1.NodeSummary
}

func (f *fakeStaticRayDashboardService) GetClusterMetadata() (*dashboard.ClusterMetadataResponse, error) {
	return &dashboard.ClusterMetadataResponse{}, nil
}

func (f *fakeStaticRayDashboardService) ListNodes() ([]v1.NodeSummary, error) {
	return f.nodes, nil
}

func (f *fakeStaticRayDashboardService) GetClusterStatus() (v1.RayAPIClusterStatus, error) {
	return v1.RayAPIClusterStatus{}, nil
}

func (f *fakeStaticRayDashboardService) GetServeApplications() (*dashboard.RayServeApplicationsResponse, error) {
	return nil, nil
}

func (f *fakeStaticRayDashboardService) UpdateServeApplications(_ dashboard.RayServeApplicationsRequest) error {
	return nil
}

func (f *fakeStaticRayDashboardService) ListActors(
	_ []dashboard.ActorFilter,
	_ bool,
	_ int,
) (*dashboard.ActorsResponse, error) {
	return nil, nil
}
