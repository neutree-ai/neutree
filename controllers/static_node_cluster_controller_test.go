package controllers

import (
	"context"
	"errors"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

type fakeStaticNodeClusterRayVerifier struct {
	err    error
	called bool
}

func (f *fakeStaticNodeClusterRayVerifier) VerifyRayCluster(_ context.Context, _ *v1.StaticNodeCluster) error {
	f.called = true

	return f.err
}

func TestStaticNodeClusterControllerReconcile(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage: newTestStaticNodeClusterStorage(objectStorage),
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
		Storage: newTestStaticNodeClusterStorage(&fakeControllerStaticNodeClusterObjectStorage{}),
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
		Storage: newTestStaticNodeClusterStorage(objectStorage),
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
		Storage: newTestStaticNodeClusterStorage(objectStorage),
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

func TestStaticNodeClusterControllerVerifiesRayClusterWhenPlanIsReady(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{
		nodes: []v1.StaticNode{
			controllerStaticClusterNode("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady),
			controllerStaticClusterNode("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady),
		},
	}
	verifier := &fakeStaticNodeClusterRayVerifier{err: errors.New("dashboard unavailable")}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Storage:     newTestStaticNodeClusterStorage(objectStorage),
		RayVerifier: verifier,
	})
	require.NoError(t, err)

	err = controller.Reconcile(controllerStaticNodeCluster())

	require.NoError(t, err)
	assert.True(t, verifier.called)
	status, ok := objectStorage.updatedStatus["7"].(*v1.StaticNodeCluster)
	require.True(t, ok)
	require.NotNil(t, status.Status)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, status.Status.Phase)
	assert.Contains(t, status.Status.ErrorMessage, "Ray cluster verification failed")
	assert.Contains(t, status.Status.ErrorMessage, "dashboard unavailable")
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
		Storage: newTestStaticNodeClusterStorage(objectStorage),
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

func newTestStaticNodeClusterStorage(objectStorage *fakeControllerStaticNodeClusterObjectStorage) storage.Storage {
	return &fakeControllerStaticNodeClusterStorage{
		MockStorage:   &storagemocks.MockStorage{},
		objectStorage: objectStorage,
	}
}

type fakeControllerStaticNodeClusterStorage struct {
	*storagemocks.MockStorage
	objectStorage *fakeControllerStaticNodeClusterObjectStorage
}

func (f *fakeControllerStaticNodeClusterStorage) ListStaticNode(storage.ListOption) ([]v1.StaticNode, error) {
	return f.objectStorage.nodes, nil
}

func (f *fakeControllerStaticNodeClusterStorage) CreateStaticNode(data *v1.StaticNode) error {
	return f.objectStorage.Create(data)
}

func (f *fakeControllerStaticNodeClusterStorage) UpdateStaticNode(id string, data *v1.StaticNode) error {
	if data != nil && data.Metadata != nil {
		return f.objectStorage.UpdateMetadata(id, data)
	}

	return f.objectStorage.Update(id, data)
}

func (f *fakeControllerStaticNodeClusterStorage) DeleteStaticNode(id string) error {
	return f.objectStorage.Delete(id, &v1.StaticNode{Kind: "StaticNode"})
}

func (f *fakeControllerStaticNodeClusterStorage) UpdateStaticNodeCluster(id string, data *v1.StaticNodeCluster) error {
	if data != nil && data.Status != nil {
		return f.objectStorage.UpdateStatus(id, data)
	}

	return f.objectStorage.Update(id, data)
}

func (f *fakeControllerStaticNodeClusterStorage) DeleteStaticNodeCluster(id string) error {
	return f.objectStorage.Delete(id, &v1.StaticNodeCluster{Kind: "StaticNodeCluster"})
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
