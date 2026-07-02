package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/staticcluster"
	"github.com/neutree-ai/neutree/internal/cluster/staticnode"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeClusterController struct {
	storage    storage.Storage
	planner    *staticcluster.Planner
	aggregator staticcluster.StatusAggregator
}

type StaticNodeClusterControllerOption struct {
	Storage                    storage.Storage
	Planner                    *staticcluster.Planner
	AcceleratorProfileProvider staticcluster.AcceleratorProfileProvider
	MetricsRemoteWriteURL      string
}

func NewStaticNodeClusterController(option *StaticNodeClusterControllerOption) (*StaticNodeClusterController, error) {
	if option == nil {
		return nil, errors.New("static node cluster controller option is required")
	}

	if option.Storage == nil {
		return nil, errors.New("storage is required")
	}

	planner := option.Planner
	if planner == nil {
		planner = &staticcluster.Planner{
			AcceleratorProfileProvider: option.AcceleratorProfileProvider,
			MetricsRemoteWriteURL:      option.MetricsRemoteWriteURL,
		}
	}

	return &StaticNodeClusterController{
		storage:    option.Storage,
		planner:    planner,
		aggregator: staticcluster.StatusAggregator{},
	}, nil
}

func (c *StaticNodeClusterController) Reconcile(obj interface{}) error {
	cluster, ok := obj.(*v1.StaticNodeCluster)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNodeCluster")
	}

	klog.V(4).Info("Reconcile static node cluster " + cluster.Metadata.WorkspaceName())

	return c.sync(context.Background(), cluster)
}

func (c *StaticNodeClusterController) sync(ctx context.Context, cluster *v1.StaticNodeCluster) error {
	if cluster == nil {
		return errors.New("static node cluster is required")
	}

	currentNodes, err := staticnode.ListByCluster(c.storage, cluster.Metadata.Workspace, cluster.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to list static nodes")
	}

	if cluster.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(cluster, currentNodes)
	}

	return c.reconcileNormal(ctx, cluster, currentNodes)
}

func (c *StaticNodeClusterController) reconcileNormal(
	ctx context.Context,
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) (reconcileErr error) {
	status := v1.StaticNodeClusterStatus{}
	defer func() {
		if reconcileErr != nil {
			status = staticNodeClusterFailedStatus(status, reconcileErr)
		}

		c.updateStatus(cluster, status, "failed to update static node cluster status", &reconcileErr)
	}()

	desiredNodePlans, err := c.planner.Plan(ctx, cluster, currentNodes)
	if err != nil {
		return err
	}

	desiredByName := make(map[string]*v1.StaticNode, len(desiredNodePlans))

	for _, nodePlan := range desiredNodePlans {
		node := nodePlan.Node
		if node == nil {
			continue
		}

		desiredByName[node.Metadata.Name] = node

		if err := upsertStaticNode(c.storage, node); err != nil {
			return errors.Wrapf(err, "failed to upsert static node %s", node.Metadata.Name)
		}
	}

	for _, node := range currentNodes {
		if node == nil {
			continue
		}

		if _, ok := desiredByName[node.Metadata.Name]; ok {
			continue
		}

		if err := softDeleteStaticNode(c.storage, node); err != nil {
			return errors.Wrapf(err, "failed to delete stale static node %s", node.Metadata.Name)
		}
	}

	latestNodes, err := staticnode.ListByCluster(c.storage, cluster.Metadata.Workspace, cluster.Metadata.Name)
	if err != nil {
		return errors.Wrap(err, "failed to list latest static nodes")
	}

	status = c.aggregator.Aggregate(cluster, latestNodes, desiredNodePlans)
	status = c.aggregator.RequireRayClusterReady(ctx, cluster, status)

	return nil
}

func (c *StaticNodeClusterController) reconcileDelete(
	cluster *v1.StaticNodeCluster,
	currentNodes []*v1.StaticNode,
) (reconcileErr error) {
	hardDeleted := false
	defer func() {
		if hardDeleted {
			return
		}

		status := staticNodeClusterDeletingStatus(len(currentNodes), reconcileErr)
		c.updateStatus(cluster, status, "failed to update static node cluster deletion status", &reconcileErr)
	}()

	if len(currentNodes) == 0 {
		if err := hardDeleteStaticNodeCluster(c.storage, cluster); err != nil {
			return err
		}

		hardDeleted = true

		return nil
	}

	isForceDelete := v1.IsForceDelete(cluster.Metadata.Annotations)

	for _, node := range currentNodes {
		if node == nil {
			continue
		}

		if isForceDelete {
			if !v1.IsForceDelete(node.Metadata.Annotations) {
				node.Metadata.Annotations = v1.WithForceDeleteAnnotation(node.Metadata.Annotations)

				if err := softDeleteStaticNode(c.storage, node); err != nil {
					return errors.Wrapf(err, "failed to mark static node %s force deleting", staticNodeName(node))
				}

				continue
			}
		}

		if node.Metadata.DeletionTimestamp != "" {
			continue
		}

		if err := softDeleteStaticNode(c.storage, node); err != nil {
			return errors.Wrapf(err, "failed to delete static node %s", staticNodeName(node))
		}
	}

	return nil
}

func (c *StaticNodeClusterController) updateStatus(
	cluster *v1.StaticNodeCluster,
	status v1.StaticNodeClusterStatus,
	message string,
	reconcileErr *error,
) {
	if err := updateStaticNodeClusterStatus(c.storage, cluster, status); err != nil {
		updateErr := errors.Wrap(err, message)
		if reconcileErr != nil && *reconcileErr == nil {
			*reconcileErr = updateErr
		}

		klog.Errorf("failed to update static node cluster %s status, err: %v", cluster.Metadata.WorkspaceName(), updateErr)
	}
}

func staticNodeName(node *v1.StaticNode) string {
	if node == nil {
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
		status.ErrorMessage += "\n" + err.Error()
	}

	return status
}

func staticNodeClusterDeletingStatus(desiredNodes int, err error) v1.StaticNodeClusterStatus {
	status := v1.StaticNodeClusterStatus{
		Phase:        v1.StaticNodeClusterPhaseProvisioning,
		DesiredNodes: desiredNodes,
		ErrorMessage: "Deleting",
	}
	if err != nil {
		return staticNodeClusterFailedStatus(status, err)
	}

	return status
}
