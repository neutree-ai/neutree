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
			if updateErr := c.store.UpdateStaticNodeClusterStatus(
				ctx,
				cluster,
				staticNodeClusterFailedStatus(plan.Status, err),
			); updateErr != nil {
				return errors.Wrap(updateErr, "failed to update static node cluster status")
			}

			return errors.Wrapf(err, "failed to upsert static node %s", node.Metadata.Name)
		}
	}

	hasStaleNodes := false
	for _, node := range currentNodes {
		if node == nil || node.Metadata == nil {
			continue
		}

		if _, ok := desiredByName[node.Metadata.Name]; ok {
			continue
		}

		hasStaleNodes = true
		if err := c.store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete stale static node %s", node.Metadata.Name)
		}
	}

	if hasStaleNodes && plan.Status.Phase == v1.StaticNodeClusterPhaseReady {
		plan.Status.Phase = v1.StaticNodeClusterPhaseProvisioning
		plan.Status.ErrorMessage = "Deleting stale static nodes"
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

	isForceDelete := v1.IsForceDelete(cluster.Metadata.Annotations)
	for _, node := range currentNodes {
		if node == nil {
			continue
		}

		if isForceDelete {
			if node.Metadata == nil {
				node.Metadata = &v1.Metadata{}
			}

			if !v1.IsForceDelete(node.Metadata.Annotations) {
				node.Metadata.Annotations = withForceDeleteAnnotation(node.Metadata.Annotations)
				if err := c.store.DeleteStaticNode(ctx, node); err != nil {
					return errors.Wrapf(err, "failed to mark static node %s force deleting", staticNodeName(node))
				}

				continue
			}
		}

		if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
			continue
		}

		if err := c.store.DeleteStaticNode(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to delete static node %s", staticNodeName(node))
		}
	}

	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseProvisioning,
		DesiredNodes: len(currentNodes),
		ErrorMessage: "Deleting",
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

func staticNodeClusterFailedStatus(
	status v1.StaticNodeClusterStatus,
	err error,
) v1.StaticNodeClusterStatus {
	status.Phase = v1.StaticNodeClusterPhaseFailed
	if err == nil {
		return status
	}

	if status.ErrorMessage == "" {
		status.ErrorMessage = err.Error()
	} else {
		status.ErrorMessage += "; " + err.Error()
	}

	return status
}
