package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type StaticNodeController struct {
	storage       storage.Storage
	runnerFactory staticNodeRunnerFactory
	reconciler    *clusterreconcile.StaticNodeReconciler
}

type StaticNodeControllerOption struct {
	Storage       storage.Storage
	RunnerFactory staticNodeRunnerFactory
	Reconciler    *clusterreconcile.StaticNodeReconciler
}

type staticNodeRunnerFactory interface {
	NewStaticNodeRunner(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error)
}

func NewStaticNodeController(option *StaticNodeControllerOption) (*StaticNodeController, error) {
	if option == nil {
		return nil, errors.New("static node controller option is required")
	}

	if option.Storage == nil {
		return nil, errors.New("storage is required")
	}

	runnerFactory := option.RunnerFactory
	if runnerFactory == nil {
		runnerFactory = clusterreconcile.NewStaticNodeSSHRunnerFactory()
	}

	reconciler := option.Reconciler
	if reconciler == nil {
		reconciler = &clusterreconcile.StaticNodeReconciler{}
	}

	if reconciler.HeadReadyChecker == nil {
		reconcilerCopy := *reconciler
		reconcilerCopy.HeadReadyChecker = &clusterreconcile.StaticNodeClusterHeadReadyChecker{Storage: option.Storage}
		reconciler = &reconcilerCopy
	}

	c := &StaticNodeController{
		storage:       option.Storage,
		runnerFactory: runnerFactory,
		reconciler:    reconciler,
	}

	return c, nil
}

func (c *StaticNodeController) Reconcile(obj interface{}) error {
	node, ok := obj.(*v1.StaticNode)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNode")
	}

	klog.V(4).Info("Reconcile static node " + node.Metadata.WorkspaceName())

	return c.sync(context.Background(), node)
}

func (c *StaticNodeController) sync(ctx context.Context, node *v1.StaticNode) error {
	if node == nil {
		return errors.New("static node is required")
	}

	if node.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(ctx, node, c.reconciler)
	}

	return c.reconcileNormal(ctx, node, c.reconciler)
}

func (c *StaticNodeController) reconcileNormal(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *clusterreconcile.StaticNodeReconciler,
) (reconcileErr error) {
	runner, err := c.runnerFactory.NewStaticNodeRunner(ctx, node)
	if err != nil {
		reconcileErr = errors.Wrap(err, "failed to create static node runner")
		status := clusterreconcile.BuildStaticNodeStatus(node, nil, reconcileErr)
		c.updateStatus(node, status, "failed to update static node status", &reconcileErr)

		return reconcileErr
	}
	defer closeStaticNodeRunner(runner)

	result := &clusterreconcile.StaticNodeReconcileResult{}

	result.Accelerator, reconcileErr = reconciler.ReconcileAccelerator(ctx, node, runner)
	c.updateStatus(node, clusterreconcile.BuildStaticNodeStatus(node, result, reconcileErr), "failed to update static node accelerator status", &reconcileErr)

	if reconcileErr != nil {
		return reconcileErr
	}

	result.Warm, reconcileErr = reconciler.ReconcileWarmImages(ctx, node, runner)
	c.updateStatus(node, clusterreconcile.BuildStaticNodeStatus(node, result, reconcileErr), "failed to update static node warm status", &reconcileErr)

	if reconcileErr != nil {
		return reconcileErr
	}

	result.Components, reconcileErr = reconciler.ReconcileComponents(ctx, node, runner)
	c.updateStatus(node, clusterreconcile.BuildStaticNodeStatus(node, result, reconcileErr), "failed to update static node component status", &reconcileErr)

	return reconcileErr
}

func (c *StaticNodeController) reconcileDelete(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *clusterreconcile.StaticNodeReconciler,
) (reconcileErr error) {
	isForceDelete := v1.IsForceDelete(node.Metadata.Annotations)
	updateStatusOnReturn := false

	defer func() {
		if !updateStatusOnReturn {
			return
		}

		status := clusterreconcile.BuildStaticNodeStatus(node, nil, reconcileErr)
		c.updateStatus(node, status, "failed to update static node status", &reconcileErr)
	}()

	runner, err := c.runnerFactory.NewStaticNodeRunner(ctx, node)
	if err != nil {
		if isForceDelete {
			klog.Warningf("failed to create static node runner during force delete best-effort cleanup: %v", err)

			return hardDeleteStaticNode(c.storage, node)
		}

		updateStatusOnReturn = true

		return errors.Wrap(err, "failed to create static node runner")
	}
	// The SSH runner owns any temporary private-key directory created from
	// spec.ssh_auth. Deferring Close here keeps remote delete paths from
	// leaking key files after runner creation succeeds.
	defer closeStaticNodeRunner(runner)

	if err := reconciler.Delete(ctx, node, runner); err != nil {
		if isForceDelete {
			klog.Warningf("static node remote cleanup failed during force delete: %v", err)

			return hardDeleteStaticNode(c.storage, node)
		}

		updateStatusOnReturn = true

		return err
	}

	return hardDeleteStaticNode(c.storage, node)
}

func (c *StaticNodeController) updateStatus(
	node *v1.StaticNode,
	status v1.StaticNodeStatus,
	message string,
	reconcileErr *error,
) {
	if err := updateStaticNodeStatus(c.storage, node, status); err != nil {
		updateErr := errors.Wrap(err, message)
		if reconcileErr != nil && *reconcileErr == nil {
			*reconcileErr = updateErr
		}

		klog.Errorf("failed to update static node %s status, err: %v", node.Metadata.WorkspaceName(), updateErr)

		return
	}

	node.Status = &status
}

func closeStaticNodeRunner(runner clusterreconcile.StaticNodeCommandRunner) {
	if runner == nil {
		return
	}

	if err := runner.Close(); err != nil {
		klog.Warningf("failed to clean up static node runner: %v", err)
	}
}
