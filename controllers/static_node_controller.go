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
	runnerFactory *clusterreconcile.StaticNodeSSHRunnerFactory
	reconciler    *clusterreconcile.StaticNodeReconciler
	newRunner     func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error)
}

type StaticNodeControllerOption struct {
	Storage       storage.Storage
	RunnerFactory *clusterreconcile.StaticNodeSSHRunnerFactory
	Reconciler    *clusterreconcile.StaticNodeReconciler
}

func NewStaticNodeController(option *StaticNodeControllerOption) (*StaticNodeController, error) {
	if option == nil {
		return nil, errors.New("static node controller option is required")
	}

	runnerFactory := option.RunnerFactory
	if runnerFactory == nil {
		runnerFactory = clusterreconcile.NewStaticNodeSSHRunnerFactory()
	}

	c := &StaticNodeController{
		storage:       option.Storage,
		runnerFactory: runnerFactory,
		reconciler:    option.Reconciler,
	}
	c.newRunner = c.runnerFactory.NewStaticNodeRunner

	return c, nil
}

func (c *StaticNodeController) Reconcile(obj interface{}) error {
	node, ok := obj.(*v1.StaticNode)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNode")
	}

	if node.Metadata != nil {
		klog.V(4).Info("Reconcile static node " + node.Metadata.WorkspaceName())
	}

	return c.sync(context.Background(), node)
}

func (c *StaticNodeController) sync(ctx context.Context, node *v1.StaticNode) error {
	if node == nil {
		return errors.New("static node is required")
	}

	if c.storage == nil {
		return errors.New("storage is required")
	}

	reconciler := c.staticNodeReconciler()
	if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
		return c.reconcileDelete(ctx, node, reconciler)
	}

	return c.reconcileNormal(ctx, node, reconciler)
}

func (c *StaticNodeController) staticNodeReconciler() *clusterreconcile.StaticNodeReconciler {
	reconciler := c.reconciler
	if reconciler == nil {
		return &clusterreconcile.StaticNodeReconciler{
			HeadReadyChecker: &clusterreconcile.StaticNodeClusterHeadReadyChecker{Storage: c.storage},
		}
	}

	if reconciler.HeadReadyChecker != nil {
		return reconciler
	}

	reconcilerCopy := *reconciler
	reconcilerCopy.HeadReadyChecker = &clusterreconcile.StaticNodeClusterHeadReadyChecker{Storage: c.storage}

	return &reconcilerCopy
}

func (c *StaticNodeController) reconcileNormal(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *clusterreconcile.StaticNodeReconciler,
) error {
	runner, err := c.newStaticNodeRunner(ctx, node)
	if err != nil {
		return errors.Wrap(err, "failed to create static node runner")
	}
	defer closeStaticNodeRunner(runner)

	result, err := reconciler.Reconcile(ctx, node, runner)
	status := clusterreconcile.BuildStaticNodeStatus(node, result, err)

	if updateErr := updateStaticNodeStatus(c.storage, node, status); updateErr != nil {
		return errors.Wrap(updateErr, "failed to update static node status")
	}

	return err
}

func (c *StaticNodeController) reconcileDelete(
	ctx context.Context,
	node *v1.StaticNode,
	reconciler *clusterreconcile.StaticNodeReconciler,
) error {
	isForceDelete := node.Metadata != nil && v1.IsForceDelete(node.Metadata.Annotations)

	runner, err := c.newStaticNodeRunner(ctx, node)
	if err != nil {
		if isForceDelete {
			klog.Warningf("failed to create static node runner during force delete best-effort cleanup: %v", err)

			return hardDeleteStaticNode(c.storage, node)
		}

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

		status := clusterreconcile.BuildStaticNodeStatus(node, nil, err)
		if updateErr := updateStaticNodeStatus(c.storage, node, status); updateErr != nil {
			return errors.Wrap(updateErr, "failed to update static node status")
		}

		return err
	}

	return hardDeleteStaticNode(c.storage, node)
}

func (c *StaticNodeController) newStaticNodeRunner(
	ctx context.Context,
	node *v1.StaticNode,
) (clusterreconcile.StaticNodeCommandRunner, error) {
	if c.newRunner == nil {
		return nil, errors.New("static node runner factory is required")
	}

	return c.newRunner(ctx, node)
}

func closeStaticNodeRunner(runner clusterreconcile.StaticNodeCommandRunner) {
	closer, ok := runner.(interface {
		Close() error
	})
	if !ok {
		return
	}

	if err := closer.Close(); err != nil {
		klog.Warningf("failed to clean up static node runner: %v", err)
	}
}
