package client

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClientListByClusterDelegatesToStore(t *testing.T) {
	store := &fakeStaticNodeStore{}
	client := NewStaticNodeClient(store)

	_, err := client.ListByCluster(context.Background(), "default", "static-a")

	require.NoError(t, err)
	assert.Equal(t, "default", store.workspace)
	assert.Equal(t, "static-a", store.clusterName)
}

func TestStaticNodeClusterClientUpdateStatusDelegatesToStore(t *testing.T) {
	store := &fakeStaticNodeStore{}
	client := NewStaticNodeClusterClient(store)
	cluster := &v1.StaticNodeCluster{}
	status := v1.StaticNodeClusterStatus{Phase: v1.StaticNodeClusterPhaseReady}

	err := client.UpdateStatus(context.Background(), cluster, status)

	require.NoError(t, err)
	assert.Same(t, cluster, store.updatedCluster)
	assert.Equal(t, status, store.clusterStatus)
}

type fakeStaticNodeStore struct {
	workspace   string
	clusterName string

	updatedCluster *v1.StaticNodeCluster
	clusterStatus  v1.StaticNodeClusterStatus
}

func (f *fakeStaticNodeStore) ListStaticNodes(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	f.workspace = workspace
	f.clusterName = clusterName

	return nil, nil
}

func (f *fakeStaticNodeStore) UpsertStaticNode(context.Context, *v1.StaticNode) error {
	return nil
}

func (f *fakeStaticNodeStore) DeleteStaticNode(context.Context, *v1.StaticNode) error {
	return nil
}

func (f *fakeStaticNodeStore) HardDeleteStaticNode(context.Context, *v1.StaticNode) error {
	return nil
}

func (f *fakeStaticNodeStore) UpdateStaticNodeStatus(context.Context, *v1.StaticNode, v1.StaticNodeStatus) error {
	return nil
}

func (f *fakeStaticNodeStore) HardDeleteStaticNodeCluster(context.Context, *v1.StaticNodeCluster) error {
	return nil
}

func (f *fakeStaticNodeStore) UpdateStaticNodeClusterStatus(
	_ context.Context,
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	f.updatedCluster = cluster
	f.clusterStatus = status

	return nil
}
