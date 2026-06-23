package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeClusterController struct {
	store      *storage.StaticNodeObjectStore
	reconciler *clusterreconcile.StaticNodeClusterReconciler
}

type StaticNodeClusterControllerOption struct {
	Store                  *storage.StaticNodeObjectStore
	Reconciler             *clusterreconcile.StaticNodeClusterReconciler
	RuntimeProfileProvider clusterreconcile.RuntimeProfileProvider
}

func NewStaticNodeClusterController(option *StaticNodeClusterControllerOption) (*StaticNodeClusterController, error) {
	if option == nil {
		return nil, errors.New("static node cluster controller option is required")
	}

	reconciler := option.Reconciler
	if reconciler == nil {
		reconciler = &clusterreconcile.StaticNodeClusterReconciler{
			RuntimeProfileProvider: option.RuntimeProfileProvider,
		}
	}

	return &StaticNodeClusterController{
		store:      option.Store,
		reconciler: reconciler,
	}, nil
}

func (c *StaticNodeClusterController) Reconcile(obj interface{}) error {
	cluster, ok := obj.(*v1.StaticNodeCluster)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNodeCluster")
	}

	if cluster.Metadata != nil {
		klog.V(4).Info("Reconcile static node cluster " + cluster.Metadata.WorkspaceName())
	}

	return c.sync(context.Background(), cluster)
}

func (c *StaticNodeClusterController) sync(ctx context.Context, cluster *v1.StaticNodeCluster) error {
	if cluster == nil || cluster.Metadata == nil {
		return errors.New("static node cluster metadata is required")
	}

	if c.store == nil {
		return errors.New("static node cluster store is required")
	}

	reconciler := c.reconciler
	if reconciler == nil {
		reconciler = &clusterreconcile.StaticNodeClusterReconciler{}
	}

	currentNodes, err := c.store.ListStaticNodes(ctx, cluster.Metadata.Workspace, cluster.Metadata.Name)
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

		if err := c.store.UpsertStaticNode(ctx, node); err != nil {
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

		if err := c.store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete stale static node %s", node.Metadata.Name)
		}
	}

	if err := c.store.UpdateStaticNodeClusterStatus(ctx, cluster, plan.Status); err != nil {
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
		return c.store.HardDeleteStaticNodeCluster(ctx, cluster)
	}

	for _, node := range currentNodes {
		if node == nil {
			continue
		}

		if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
			continue
		}

		if err := c.store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete static node %s", staticNodeName(node))
		}
	}

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseStopping,
		DesiredNodes: len(currentNodes),
	}
	if err := c.store.UpdateStaticNodeClusterStatus(ctx, cluster, status); err != nil {
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
