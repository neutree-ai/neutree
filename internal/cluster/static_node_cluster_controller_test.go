package cluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestStaticNodeClusterControllerReconcileSyncsStaticNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	store := &fakeStaticNodeClusterStore{
		currentNodes: []*v1.StaticNode{
			staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, []v1.NodeComponentStatus{
				readyComponent(nodeExporterComponentName),
			}),
			staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
		},
	}

	err := (&StaticNodeClusterController{Store: store}).Reconcile(context.Background(), cluster, nil)

	require.NoError(t, err)
	require.Len(t, store.upsertedNodes, 2)
	assert.Equal(t, "head-0", store.upsertedNodes[0].Metadata.Name)
	assert.Equal(t, "worker-0", store.upsertedNodes[1].Metadata.Name)
	require.Len(t, store.deletedNodes, 1)
	assert.Equal(t, "stale-0", store.deletedNodes[0].Metadata.Name)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, store.updatedStatus.Phase)
	assert.Equal(t, 1, store.updatedStatus.ReadyNodes)
	assert.Equal(t, "default", store.listWorkspace)
	assert.Equal(t, "static-a", store.listClusterName)
}

type fakeStaticNodeClusterStore struct {
	currentNodes    []*v1.StaticNode
	upsertedNodes   []*v1.StaticNode
	deletedNodes    []*v1.StaticNode
	updatedStatus   v1.StaticNodeClusterStatus
	listWorkspace   string
	listClusterName string
}

func (f *fakeStaticNodeClusterStore) ListStaticNodes(
	_ context.Context,
	workspace string,
	clusterName string,
) ([]*v1.StaticNode, error) {
	f.listWorkspace = workspace
	f.listClusterName = clusterName

	return f.currentNodes, nil
}

func (f *fakeStaticNodeClusterStore) UpsertStaticNode(_ context.Context, node *v1.StaticNode) error {
	f.upsertedNodes = append(f.upsertedNodes, node)

	return nil
}

func (f *fakeStaticNodeClusterStore) DeleteStaticNode(_ context.Context, node *v1.StaticNode) error {
	f.deletedNodes = append(f.deletedNodes, node)

	return nil
}

func (f *fakeStaticNodeClusterStore) UpdateStaticNodeClusterStatus(
	_ context.Context,
	_ *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
) error {
	f.updatedStatus = status

	return nil
}
