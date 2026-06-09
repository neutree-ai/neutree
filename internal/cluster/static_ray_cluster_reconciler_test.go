package cluster

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestStaticRayClusterReconcilerCreatesStaticNodeCluster(t *testing.T) {
	store := &fakeStaticRayClusterStore{
		imageRegistries: []v1.ImageRegistry{connectedImageRegistry("registry.example.com", "neutree")},
	}
	cluster := testStaticRayCluster()
	reconciler, err := newStaticRayClusterReconcile(store, "http://vm:8480/insert/0/prometheus/")
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
	assert.Equal(t, "static-a-head", created.Spec.Head.NodeName)
	require.Len(t, created.Spec.Nodes, 2)
	assert.Equal(t, "10.0.0.10", created.Spec.Nodes[0].IP)
	assert.Equal(t, v1.StaticNodeRoleHead, created.Spec.Nodes[0].Role)
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
	reconciler, err := newStaticRayClusterReconcile(store, "http://vm:8480/insert/0/prometheus/")
	require.NoError(t, err)

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
}

type fakeStaticRayClusterStore struct {
	imageRegistries []v1.ImageRegistry
	staticClusters  []v1.StaticNodeCluster
	created         []*v1.StaticNodeCluster
	updated         []*v1.StaticNodeCluster
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
	data.ID = 7
	f.updated = append(f.updated, data)

	return nil
}

func (f *fakeStaticRayClusterStore) DeleteStaticNodeCluster(id string) error {
	f.deletedIDs = append(f.deletedIDs, id)

	return nil
}

func testStaticRayCluster() *v1.Cluster {
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
