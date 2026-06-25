package controllers

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterControllerReconcile(t *testing.T) {
	objectStorage := &fakeControllerStaticNodeClusterObjectStorage{}
	controller, err := NewStaticNodeClusterController(&StaticNodeClusterControllerOption{
		Store: storage.NewStaticNodeObjectStore(objectStorage),
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
		Store: storage.NewStaticNodeObjectStore(&fakeControllerStaticNodeClusterObjectStorage{}),
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
		Store: storage.NewStaticNodeObjectStore(objectStorage),
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

type fakeControllerStaticNodeClusterObjectStorage struct {
	nodes         []v1.StaticNode
	created       []scheme.Object
	updatedStatus map[string]scheme.Object
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

func (f *fakeControllerStaticNodeClusterObjectStorage) UpdateMetadata(_ string, _ scheme.Object) error {
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
