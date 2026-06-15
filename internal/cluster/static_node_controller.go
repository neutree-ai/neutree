package cluster

import (
	"context"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type StaticNodeStore interface {
	UpdateStaticNodeStatus(ctx context.Context, node *v1.StaticNode, status v1.StaticNodeStatus) error
	HardDeleteStaticNode(ctx context.Context, node *v1.StaticNode) error
}

type StaticNodeRunnerFactory interface {
	NewStaticNodeRunner(ctx context.Context, node *v1.StaticNode) (StaticNodeCommandRunner, error)
}

type StaticNodeController struct {
	Store         StaticNodeStore
	RunnerFactory StaticNodeRunnerFactory
	Reconciler    *StaticNodeReconciler
}

func (c *StaticNodeController) Reconcile(ctx context.Context, node *v1.StaticNode) error {
	if node == nil {
		return errors.New("static node is required")
	}

	if c.Store == nil {
		return errors.New("static node store is required")
	}

	reconciler := c.Reconciler
	if reconciler == nil {
		reconciler = &StaticNodeReconciler{}
	}

	var runner StaticNodeCommandRunner

	if needsStaticNodeRunner(node) {
		if c.RunnerFactory == nil {
			return errors.New("static node runner factory is required")
		}

		nodeRunner, err := c.RunnerFactory.NewStaticNodeRunner(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to create static node runner")
		}

		runner = nodeRunner
	}

	if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
		if err := reconciler.Delete(ctx, node, runner); err != nil {
			status := buildStaticNodeStatus(node, nil, err)
			if updateErr := c.Store.UpdateStaticNodeStatus(ctx, node, status); updateErr != nil {
				return errors.Wrap(updateErr, "failed to update static node status")
			}

			return err
		}

		return c.Store.HardDeleteStaticNode(ctx, node)
	}

	result, err := reconciler.Reconcile(ctx, node, runner)
	status := buildStaticNodeStatus(node, result, err)

	if updateErr := c.Store.UpdateStaticNodeStatus(ctx, node, status); updateErr != nil {
		return errors.Wrap(updateErr, "failed to update static node status")
	}

	return err
}

func needsStaticNodeRunner(node *v1.StaticNode) bool {
	if node == nil || node.Spec == nil {
		return false
	}

	if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
		return true
	}

	if node.Status == nil || node.Status.Accelerator == nil {
		return true
	}

	if node.Spec.Warm != nil && len(node.Spec.Warm.Images) > 0 {
		return true
	}

	return len(node.Spec.Components) > 0
}

func buildStaticNodeStatus(node *v1.StaticNode, result *StaticNodeReconcileResult, reconcileErr error) v1.StaticNodeStatus {
	status := v1.StaticNodeStatus{}
	if node != nil && node.Status != nil {
		status = *node.Status
	}

	if result != nil {
		status.Accelerator = result.Accelerator
		status.Warm = result.Warm
		status.Components = result.Components
	}

	if reconcileErr != nil {
		status.Phase = v1.StaticNodePhaseFailed
		status.ErrorMessage = reconcileErr.Error()

		return status
	}

	status.ErrorMessage = ""

	if status.Warm != nil && !status.Warm.Ready {
		status.Phase = v1.StaticNodePhaseWarming

		return status
	}

	if !allNodeComponentsReady(status.Components) {
		status.Phase = v1.StaticNodePhaseReconciling

		return status
	}

	status.Phase = v1.StaticNodePhaseReady
	status.LastTransitionTime = time.Now().UTC().Format(time.RFC3339)

	return status
}

func allNodeComponentsReady(components []v1.NodeComponentStatus) bool {
	for _, component := range components {
		if !component.Ready || component.Phase != v1.NodeComponentPhaseRunning {
			return false
		}
	}

	return true
}
