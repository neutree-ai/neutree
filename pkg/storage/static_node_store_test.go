package storage

import (
	"context"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

func TestStaticNodeObjectStoreListStaticNodesFiltersByWorkspaceAndCluster(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{
		nodes: []v1.StaticNode{
			testStorageStaticNode(1, "default", "head-0", "static-a"),
			testStorageStaticNode(2, "default", "head-1", "static-b"),
			testStorageStaticNode(3, "other", "head-2", "static-a"),
		},
	}
	store := NewStaticNodeObjectStore(objectStorage)

	nodes, err := store.ListStaticNodes(context.Background(), "default", "static-a")

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	assert.Equal(t, "head-0", nodes[0].Metadata.Name)
}

type fakeStaticNodeObjectStorage struct {
	nodes              []v1.StaticNode
	created            []scheme.Object
	updatedMetadataIDs []string
	updatedMetadata    map[string]scheme.Object
	updatedSpecIDs     []string
	updatedStatus      map[string]scheme.Object
	deletedIDs         []string
}

func TestStaticNodeObjectStoreUpsertStaticNode(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{
		nodes: []v1.StaticNode{
			testStorageStaticNode(11, "default", "head-0", "static-a"),
		},
	}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.UpsertStaticNode(context.Background(), &v1.StaticNode{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "head-0",
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.10",
		},
	})

	require.NoError(t, err)
	assert.Empty(t, objectStorage.created)
	assert.Equal(t, []string{"11"}, objectStorage.updatedMetadataIDs)
	assert.Equal(t, []string{"11"}, objectStorage.updatedSpecIDs)

	err = store.UpsertStaticNode(context.Background(), &v1.StaticNode{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "worker-0",
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-a",
			IP:      "10.0.0.11",
		},
	})

	require.NoError(t, err)
	require.Len(t, objectStorage.created, 1)
	created, ok := objectStorage.created[0].(*v1.StaticNode)
	require.True(t, ok)
	assert.Equal(t, "StaticNode", created.Kind)
	assert.Equal(t, "v1", created.APIVersion)
	assert.Equal(t, "worker-0", created.Metadata.Name)
}

func TestStaticNodeObjectStoreUpsertStaticNodeRejectsClusterConflict(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{
		nodes: []v1.StaticNode{
			testStorageStaticNode(11, "default", "172.20.172.120", "static-a"),
		},
	}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.UpsertStaticNode(context.Background(), &v1.StaticNode{
		Metadata: &v1.Metadata{
			Workspace: "default",
			Name:      "172.20.172.120",
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: "static-b",
			IP:      "172.20.172.120",
		},
	})

	require.Error(t, err)
	assert.Contains(t, err.Error(), "already owned by static node cluster static-a")
	assert.Empty(t, objectStorage.created)
	assert.Empty(t, objectStorage.updatedMetadataIDs)
	assert.Empty(t, objectStorage.updatedSpecIDs)
}

func TestStaticNodeObjectStoreDeleteStaticNodeSoftDeletes(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.DeleteStaticNode(context.Background(), &v1.StaticNode{ID: 12})

	require.NoError(t, err)
	assert.Empty(t, objectStorage.deletedIDs)
	require.Len(t, objectStorage.updatedMetadata, 1)
	updated, ok := objectStorage.updatedMetadata["12"].(*v1.StaticNode)
	require.True(t, ok)
	require.NotNil(t, updated.Metadata)
	assert.NotEmpty(t, updated.Metadata.DeletionTimestamp)
}

func TestStaticNodeObjectStoreHardDeleteStaticNode(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.HardDeleteStaticNode(context.Background(), &v1.StaticNode{ID: 12})

	require.NoError(t, err)
	assert.Equal(t, []string{"12"}, objectStorage.deletedIDs)
}

func TestStaticNodeObjectStoreHardDeleteStaticNodeCluster(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.HardDeleteStaticNodeCluster(context.Background(), &v1.StaticNodeCluster{ID: 13})

	require.NoError(t, err)
	assert.Equal(t, []string{"13"}, objectStorage.deletedIDs)
}

func TestStaticNodeObjectStoreUpdateStatus(t *testing.T) {
	objectStorage := &fakeStaticNodeObjectStorage{}
	store := NewStaticNodeObjectStore(objectStorage)

	err := store.UpdateStaticNodeClusterStatus(
		context.Background(),
		&v1.StaticNodeCluster{ID: 7},
		v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady},
	)
	require.NoError(t, err)

	err = store.UpdateStaticNodeStatus(
		context.Background(),
		&v1.StaticNode{ID: 8},
		v1.StaticNodeStatus{Phase: v1.StaticNodePhaseReady},
	)
	require.NoError(t, err)

	require.Len(t, objectStorage.updatedStatus, 2)
	assert.IsType(t, &v1.StaticNodeCluster{}, objectStorage.updatedStatus["7"])
	assert.IsType(t, &v1.StaticNode{}, objectStorage.updatedStatus["8"])
}

func (f *fakeStaticNodeObjectStorage) Create(data scheme.Object) error {
	f.created = append(f.created, data)

	return nil
}

func (f *fakeStaticNodeObjectStorage) Update(id string, data scheme.Object) error {
	return errors.New("unexpected update " + id + " for " + data.GetKind())
}

func (f *fakeStaticNodeObjectStorage) Delete(id string, _ scheme.Object) error {
	f.deletedIDs = append(f.deletedIDs, id)

	return nil
}

func (f *fakeStaticNodeObjectStorage) Get(_ string, _ scheme.Object) error {
	return errors.New("unexpected get")
}

func (f *fakeStaticNodeObjectStorage) List(obj scheme.ObjectList, _ ListOption) error {
	items := make([]scheme.Object, 0, len(f.nodes))
	for i := range f.nodes {
		node := f.nodes[i]
		items = append(items, &node)
	}

	obj.SetItems(items)

	return nil
}

func (f *fakeStaticNodeObjectStorage) UpdateMetadata(id string, data scheme.Object) error {
	f.updatedMetadataIDs = append(f.updatedMetadataIDs, id)
	if f.updatedMetadata == nil {
		f.updatedMetadata = map[string]scheme.Object{}
	}

	f.updatedMetadata[id] = data

	return nil
}

func (f *fakeStaticNodeObjectStorage) UpdateSpec(id string, _ scheme.Object) error {
	f.updatedSpecIDs = append(f.updatedSpecIDs, id)

	return nil
}

func (f *fakeStaticNodeObjectStorage) UpdateStatus(id string, data scheme.Object) error {
	if f.updatedStatus == nil {
		f.updatedStatus = map[string]scheme.Object{}
	}

	f.updatedStatus[id] = data

	return nil
}

func testStorageStaticNode(id int, workspace, name, clusterName string) v1.StaticNode {
	return v1.StaticNode{
		ID:         id,
		APIVersion: "v1",
		Kind:       "StaticNode",
		Metadata: &v1.Metadata{
			Workspace: workspace,
			Name:      name,
		},
		Spec: &v1.StaticNodeSpec{
			Cluster: clusterName,
		},
	}
}
