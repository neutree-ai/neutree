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
	store         *storage.StaticNodeObjectStore
	runnerFactory *StaticNodeSSHRunnerFactory
	reconciler    *clusterreconcile.StaticNodeReconciler
	newRunner     func(context.Context, *v1.StaticNode) (clusterreconcile.StaticNodeCommandRunner, error)
}

type StaticNodeControllerOption struct {
	Store         *storage.StaticNodeObjectStore
	RunnerFactory *StaticNodeSSHRunnerFactory
	Reconciler    *clusterreconcile.StaticNodeReconciler
}

func NewStaticNodeController(option *StaticNodeControllerOption) (*StaticNodeController, error) {
	if option == nil {
		return nil, errors.New("static node controller option is required")
	}

	runnerFactory := option.RunnerFactory
	if runnerFactory == nil {
		runnerFactory = NewStaticNodeSSHRunnerFactory()
	}

	c := &StaticNodeController{
		store:         option.Store,
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

	if c.store == nil {
		return errors.New("static node store is required")
	}

	reconciler := c.reconciler
	if reconciler == nil {
		reconciler = &clusterreconcile.StaticNodeReconciler{}
	}

	if node.Metadata != nil &&
		node.Metadata.DeletionTimestamp != "" &&
		v1.IsForceDelete(node.Metadata.Annotations) {
		return c.store.HardDeleteStaticNode(ctx, node)
	}

	var runner clusterreconcile.StaticNodeCommandRunner

	if clusterreconcile.NeedsStaticNodeRunner(node, reconciler) {
		if c.newRunner == nil {
			return errors.New("static node runner factory is required")
		}

		nodeRunner, err := c.newRunner(ctx, node)
		if err != nil {
			return errors.Wrap(err, "failed to create static node runner")
		}

		runner = nodeRunner
	}

	if node.Metadata != nil && node.Metadata.DeletionTimestamp != "" {
		if err := reconciler.Delete(ctx, node, runner); err != nil {
			status := clusterreconcile.BuildStaticNodeStatus(node, nil, err)
			if updateErr := c.store.UpdateStaticNodeStatus(ctx, node, status); updateErr != nil {
				return errors.Wrap(updateErr, "failed to update static node status")
			}

			return err
		}

		return c.store.HardDeleteStaticNode(ctx, node)
	}

	result, err := reconciler.Reconcile(ctx, node, runner)
	status := clusterreconcile.BuildStaticNodeStatus(node, result, err)

	if updateErr := c.store.UpdateStaticNodeStatus(ctx, node, status); updateErr != nil {
		return errors.Wrap(updateErr, "failed to update static node status")
	}

	return err
}
