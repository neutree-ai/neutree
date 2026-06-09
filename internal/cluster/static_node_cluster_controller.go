package cluster

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type StaticNodeClusterStore interface {
	ListStaticNodes(ctx context.Context, workspace, clusterName string) ([]*v1.StaticNode, error)
	UpsertStaticNode(ctx context.Context, node *v1.StaticNode) error
	DeleteStaticNode(ctx context.Context, node *v1.StaticNode) error
	UpdateStaticNodeClusterStatus(ctx context.Context, cluster *v1.StaticNodeCluster, status v1.StaticNodeClusterStatus) error
}

type StaticNodeClusterController struct {
	Store      StaticNodeClusterStore
	Reconciler *StaticNodeClusterReconciler
}

func (c *StaticNodeClusterController) Reconcile(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	acceleratorProfiles map[string]*v1.AcceleratorProfile,
) error {
	if cluster == nil || cluster.Metadata == nil {
		return errors.New("static node cluster metadata is required")
	}

	if c.Store == nil {
		return errors.New("static node cluster store is required")
	}

	reconciler := c.Reconciler
	if reconciler == nil {
		reconciler = &StaticNodeClusterReconciler{}
	}

	currentNodes, err := c.Store.ListStaticNodes(ctx, cluster.Metadata.Workspace, cluster.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to list static nodes")
	}

	plan, err := reconciler.Plan(cluster, currentNodes, acceleratorProfiles)
	if err != nil {
		return err
	}

	headReady := staticNodeClusterHeadReady(cluster, currentNodes)
	desiredByName := make(map[string]*v1.StaticNode, len(plan.DesiredNodes))

	for _, node := range plan.DesiredNodes {
		if node == nil || node.Metadata == nil {
			continue
		}

		if deferStaticWorkerNode(cluster, node, headReady) {
			continue
		}

		desiredByName[node.Metadata.Name] = node

		if err := c.Store.UpsertStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to upsert static node %s", node.Metadata.Name)
		}
	}

	for _, node := range currentNodes {
		if node == nil || node.Metadata == nil {
			continue
		}

		if _, ok := desiredByName[node.Metadata.Name]; ok {
			continue
		}

		if err := c.Store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete stale static node %s", node.Metadata.Name)
		}
	}

	if err := c.Store.UpdateStaticNodeClusterStatus(ctx, cluster, plan.Status); err != nil {
		return errors.Wrap(err, "failed to update static node cluster status")
	}

	return nil
}

func staticNodeClusterHeadReady(cluster *v1.StaticNodeCluster, nodes []*v1.StaticNode) bool {
	if cluster == nil || cluster.Spec == nil || cluster.Spec.Head.NodeName == "" {
		return false
	}

	for _, node := range nodes {
		if node == nil || node.Metadata == nil || node.Status == nil {
			continue
		}

		if node.Metadata.Name == cluster.Spec.Head.NodeName {
			return node.Status.Phase == v1.StaticNodePhaseReady
		}
	}

	return false
}

func deferStaticWorkerNode(cluster *v1.StaticNodeCluster, node *v1.StaticNode, headReady bool) bool {
	if headReady || cluster == nil || cluster.Spec == nil || node == nil || node.Metadata == nil || node.Spec == nil {
		return false
	}

	if node.Metadata.Name == cluster.Spec.Head.NodeName {
		return false
	}

	return node.Spec.Role == v1.StaticNodeRoleWorker
}
