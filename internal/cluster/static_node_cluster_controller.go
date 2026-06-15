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
	HardDeleteStaticNodeCluster(ctx context.Context, cluster *v1.StaticNodeCluster) error
	UpdateStaticNodeClusterStatus(ctx context.Context, cluster *v1.StaticNodeCluster, status v1.StaticNodeClusterStatus) error
}

type StaticNodeClusterController struct {
	Store      StaticNodeClusterStore
	Reconciler *StaticNodeClusterReconciler
}

func (c *StaticNodeClusterController) Reconcile(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
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

	if cluster.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(ctx, cluster, currentNodes)
	}

	plan, err := reconciler.Plan(ctx, cluster, currentNodes)
	if err != nil {
		return err
	}

	desiredByName := make(map[string]*v1.StaticNode, len(plan.DesiredNodes))

	for _, node := range plan.DesiredNodes {
		if node == nil || node.Metadata == nil {
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

func (c *StaticNodeClusterController) reconcileDelete(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) error {
	if len(currentNodes) == 0 {
		return c.Store.HardDeleteStaticNodeCluster(ctx, cluster)
	}

	for _, node := range currentNodes {
		if node == nil {
			continue
		}

		if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
			continue
		}

		if err := c.Store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete static node %s", staticNodeName(node))
		}
	}

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseStopping,
		DesiredNodes: len(currentNodes),
	}
	if err := c.Store.UpdateStaticNodeClusterStatus(ctx, cluster, status); err != nil {
		return errors.Wrap(err, "failed to update static node cluster deletion status")
	}

	return nil
}

func staticNodeName(node *v1.StaticNode) string {
	if node == nil || node.Metadata == nil {
		return ""
	}

	return node.Metadata.Name
}
