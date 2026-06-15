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
			staticNodeStatusWithAccelerator(
				"head-0",
				v1.StaticNodeRoleHead,
				v1.StaticNodePhaseReady,
				true,
				nvidiaAcceleratorStatus(),
				[]v1.NodeComponentStatus{
					readyComponent(nodeExporterComponentName),
					readyComponent(vmagentComponentName),
				},
			),
			staticNodeStatus("stale-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
		},
	}
	reconciler := &StaticNodeClusterReconciler{
		RuntimeProfileProvider: fakeRuntimeProfileProvider{},
	}

	err := (&StaticNodeClusterController{Store: store, Reconciler: reconciler}).Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.Len(t, store.upsertedNodes, 2)
	assert.Equal(t, "head-0", store.upsertedNodes[0].Metadata.Name)
	assert.Equal(t, "worker-0", store.upsertedNodes[1].Metadata.Name)
	require.Len(t, store.deletedNodes, 1)
	assert.Equal(t, "stale-0", store.deletedNodes[0].Metadata.Name)
	assert.Equal(t, v1.StaticNodeClusterPhaseDegraded, store.updatedStatus.Phase)
	assert.Equal(t, 1, store.updatedStatus.ReadyNodes)
	assert.Equal(t, "default", store.listWorkspace)
	assert.Equal(t, "static-a", store.listClusterName)
}

func TestStaticNodeClusterControllerReconcileCreatesWorkersBeforeHeadReady(t *testing.T) {
	cluster := testStaticNodeCluster()
	store := &fakeStaticNodeClusterStore{}

	err := (&StaticNodeClusterController{Store: store}).Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.Len(t, store.upsertedNodes, 2)
	assert.Equal(t, "head-0", store.upsertedNodes[0].Metadata.Name)
	assert.Equal(t, v1.StaticNodeRoleHead, store.upsertedNodes[0].Spec.Role)
	assert.Equal(t, "worker-0", store.upsertedNodes[1].Metadata.Name)
	assert.Equal(t, v1.StaticNodeRoleWorker, store.upsertedNodes[1].Spec.Role)
	assert.Empty(t, store.deletedNodes)
	assert.Equal(t, v1.StaticNodeClusterPhaseProvisioning, store.updatedStatus.Phase)
	assert.Equal(t, 0, store.updatedStatus.ReadyNodes)
	assert.False(t, store.updatedStatus.HeadReady)
}

func TestStaticNodeClusterControllerReconcileDeletionSoftDeletesNodes(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Metadata.DeletionTimestamp = "2026-06-15T00:00:00Z"
	store := &fakeStaticNodeClusterStore{
		currentNodes: []*v1.StaticNode{
			staticNodeStatus("head-0", v1.StaticNodeRoleHead, v1.StaticNodePhaseReady, true, nil),
			staticNodeStatus("worker-0", v1.StaticNodeRoleWorker, v1.StaticNodePhaseReady, true, nil),
		},
	}

	err := (&StaticNodeClusterController{Store: store}).Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.Len(t, store.deletedNodes, 2)
	assert.Empty(t, store.upsertedNodes)
	assert.Empty(t, store.hardDeletedClusters)
	assert.Equal(t, v1.StaticNodeClusterPhaseStopping, store.updatedStatus.Phase)
	assert.Equal(t, 2, store.updatedStatus.DesiredNodes)
}

func TestStaticNodeClusterControllerReconcileDeletionHardDeletesClusterAfterNodesGone(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.ID = 8
	cluster.Metadata.DeletionTimestamp = "2026-06-15T00:00:00Z"
	store := &fakeStaticNodeClusterStore{}

	err := (&StaticNodeClusterController{Store: store}).Reconcile(context.Background(), cluster)

	require.NoError(t, err)
	require.Len(t, store.hardDeletedClusters, 1)
	assert.Equal(t, 8, store.hardDeletedClusters[0].ID)
	assert.Empty(t, store.deletedNodes)
}

type fakeStaticNodeClusterStore struct {
	currentNodes        []*v1.StaticNode
	upsertedNodes       []*v1.StaticNode
	deletedNodes        []*v1.StaticNode
	hardDeletedClusters []*v1.StaticNodeCluster
	updatedStatus       v1.StaticNodeClusterStatus
	listWorkspace       string
	listClusterName     string
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

func (f *fakeStaticNodeClusterStore) HardDeleteStaticNodeCluster(_ context.Context, cluster *v1.StaticNodeCluster) error {
	f.hardDeletedClusters = append(f.hardDeletedClusters, cluster)

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
