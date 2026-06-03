package cluster

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type StaticNodeStore interface {
	UpdateStaticNodeStatus(ctx context.Context, node *v1.StaticNode, status v1.StaticNodeStatus) error
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

	if node.Spec != nil && node.Spec.Warm != nil && len(node.Spec.Warm.Images) > 0 {
		if c.RunnerFactory == nil {
			return errors.New("static node runner factory is required")
		}

		nodeRunner, err := c.RunnerFactory.NewStaticNodeRunner(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to create static node runner")
		}

		runner = nodeRunner
	}

	warmStatus, err := reconciler.ReconcileWarmImages(ctx, node, runner)
	status := buildStaticNodeStatus(node, warmStatus, err)

	if updateErr := c.Store.UpdateStaticNodeStatus(ctx, node, status); updateErr != nil {
		return errors.Wrap(updateErr, "failed to update static node status")
	}

	return err
}

func buildStaticNodeStatus(node *v1.StaticNode, warmStatus *v1.WarmStatus, reconcileErr error) v1.StaticNodeStatus {
	status := v1.StaticNodeStatus{}
	if node != nil && node.Status != nil {
		status = *node.Status
	}

	status.Warm = warmStatus
	if reconcileErr != nil {
		status.Phase = v1.StaticNodePhaseFailed
		status.ErrorMessage = reconcileErr.Error()

		return status
	}

	if warmStatus == nil || warmStatus.Ready {
		status.Phase = v1.StaticNodePhaseReady
	} else {
		status.Phase = v1.StaticNodePhaseWarming
	}

	return status
}
