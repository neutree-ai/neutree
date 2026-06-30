package controllers

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	staticclient "github.com/neutree-ai/neutree/internal/client"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterControllerReconcile(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Nodes:    newTestStaticNodeClusterNodeClient(objectStorage),
		Clusters: newTestStaticNodeClusterClient(objectStorage),
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	require.Len(t, objectStorage.created, 2)
	assert.Equal(t, "head-0", objectStorage.created[0].(*v1.StaticNode).Metadata.Name)
	assert.Equal(t, "worker-0", objectStorage.created[1].(*v1.StaticNode).Metadata.Name)
	status, ok := objectStorage.updatedStatus["7"].(*v1.StaticNodeCluster)
	require.True(t, ok)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
}

func TestStaticNodeClusterControllerReconcileRejectsWrongType(t *testing.T) {
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Nodes:    newTestStaticNodeClusterNodeClient(&fakeControllerStaticNodeClusterObjectStorage{}),
		Clusters: newTestStaticNodeClusterClient(&fakeControllerStaticNodeClusterObjectStorage{}),
	})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.StaticNode{})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "failed to assert obj to *v1.StaticNodeCluster")
}

func TestStaticNodeClusterControllerReconcileRecordsNodeOwnerConflict(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{
		nodes: []v1.StaticNode{
			{
				ID: 11,
				Metadata: &v1.Metadata{
					Workspace: "default",
					Name:      "head-0",
				},
				Spec: &v1.StaticNodeSpec{
					Cluster: "static-a",
				},
			},
		},
	}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Nodes:    newTestStaticNodeClusterNodeClient(objectStorage),
		Clusters: newTestStaticNodeClusterClient(objectStorage),
	})
	require.NoError(t, err)

	cluster := controllerStaticNodeCluster()
	cluster.Metadata.Name = "static-b"
	cluster.Spec.Nodes = []v1.StaticNodeClusterNodeSpec{
		{
			Name: "head-0",
			IP:   "10.0.0.10",
			Role: v1.StaticNodeRoleHead,
		},
	}

	err = controller.Reconcile(cluster)

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already owned by static node cluster static-a")
	status, ok := objectStorage.updatedStatus["7"].(*v1.StaticNodeCluster)
	require.True(t, ok)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseFailed, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "already owned by static node cluster static-a")
}

func TestStaticNodeClusterControllerReconcileWaitsForStaleNodeDeletion(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{
		nodes: []v1.StaticNode{
			controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
			controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
			controllerStaticClusterNode("worker-stale", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
		},
	}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Nodes:    newTestStaticNodeClusterNodeClient(objectStorage),
		Clusters: newTestStaticNodeClusterClient(objectStorage),
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	status, ok := objectStorage.updatedStatus["7"].(*v1.StaticNodeCluster)
	require.True(t, ok)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
	assert.Equal(t, "Deleting stale static nodes", status.Status.ErrorMessage)
	assert.Contains(t, objectStorage.updatedMetadata, "13")
}

func TestStaticNodeClusterControllerDeletePropagatesForceDeleteToNodes(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{
		nodes: []v1.StaticNode{
			controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
			func() v1.StaticNode {
				node := controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady)
				node.Metadata.DeletionTimestamp = "2026-06-30T00:00:00Z"

				return node
			}(),
		},
	}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Nodes:    newTestStaticNodeClusterNodeClient(objectStorage),
		Clusters: newTestStaticNodeClusterClient(objectStorage),
	})
	require.NoError(t, err)

	cluster := controllerStaticNodeCluster()
	cluster.Metadata.DeletionTimestamp = "2026-06-30T00:00:00Z"
	cluster.Metadata.Annotations = map[string]string{"neutree.ai/force-delete": "true"}

	err = controller.Reconcile(cluster)

	require.NoError(t, err)
	for _, id := range []string{"11", "12"} {
		updated, ok := objectStorage.updatedMetadata[id].(*v1.StaticNode)
		require.True(t, ok)
		require.NotNil(t, updated.Metadata)
		assert.True(t, v1.IsForceDelete(updated.Metadata.Annotations))
		assert.NotEmpty(t, updated.Metadata.DeletionTimestamp)
	}
}

func newTestStaticNodeClusterNodeClient(objectStorage storage.ObjectStorage) *staticclient.StaticNodeClient {
	return staticclient.NewStaticNodeClient(storage.NewStaticNodeObjectStore(objectStorage))
}

func newTestStaticNodeClusterClient(objectStorage storage.ObjectStorage) *staticclient.StaticNodeClusterClient {
	return staticclient.NewStaticNodeClusterClient(storage.NewStaticNodeObjectStore(objectStorage))
}

type fakeControllerStaticNodeClusterObjectStorage struct {
	nodes           []v1.StaticNode
	created         []scheme.Object
	updatedMetadata map[string]scheme.Object
	updatedStatus   map[string]scheme.Object
}

func (f *fakeControllerStaticNodeClusterObjectStorage) Create(data scheme.Object) error {
	f.created = append(f.created, data)

	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) Update(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) Delete(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) Get(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) List(obj scheme.ObjectList, _ storage.ListOption) error {
	items := make([]scheme.Object, 0, len(f.nodes))
	for i := range f.nodes {
		node := f.nodes[i]
		items = append(items, &node)
	}

	obj.SetItems(items)

	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) UpdateMetadata(id string, data scheme.Object) error {
	if f.updatedMetadata == nil {
		f.updatedMetadata = map[string]scheme.Object{}
	}

	f.updatedMetadata[id] = data

	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) UpdateSpec(_ string, _ scheme.Object) error {
	return nil
}

func (f *fakeControllerStaticNodeClusterObjectStorage) UpdateStatus(id string, data scheme.Object) error {
	if f.updatedStatus == nil {
		f.updatedStatus = map[string]scheme.Object{}
	}

	f.updatedStatus[id] = data

	return nil
}

func controllerStaticNodeCluster() *v1.StaticNodeCluster {
	return &v1.StaticNodeCluster{
		ID: 7,
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "static-a",
		},
		Spec: &v1.StaticNodeClusterSpec{
			Version:       "v1.2.0",
			ImageRegistry: "registry.example.com/neutree",
			Nodes: []v1.StaticNodeClusterNodeSpec{
				{
					Name: "head-0",
					IP:   "10.0.0.10",
					Role: v1.StaticNodeRoleHead,
				},
				{
					Name: "worker-0",
					IP:   "10.0.0.11",
					Role: v1.StaticNodeRoleWorker,
				},
			},
		},
	}
}

func controllerStaticClusterNode(name string, role v1.StaticNodeRole, phase v1.StaticNodePhase) v1.StaticNode {
	id := 11
	switch name {
	case "worker-0":
		id = 12
	case "worker-stale":
		id = 13
	}

	return v1.StaticNode{
		ID: id,
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      name,
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
			Role:    role,
		},
		Status: &v1.StaticNodeStatus{
			Phase: phase,
			Warm:  &v1.WarmStatus{Ready: true},
		},
	}
}
