package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
)

type StaticNodeController struct {
	syncHandler func(node *v1.StaticNode) error
}

type StaticNodeControllerOption struct {
	Store               clusterreconcile.StaticNodeStore
	RunnerFactory       clusterreconcile.StaticNodeRunnerFactory
	Reconciler          *clusterreconcile.StaticNodeReconciler
	ReconcileController *clusterreconcile.StaticNodeController
}

func NewStaticNodeController(option *StaticNodeControllerOption) (*StaticNodeController, error) {
	if option == nil {
		return nil, errors.New("static node controller option is required")
	}

	reconcileController := option.ReconcileController
	if reconcileController == nil {
		reconcileController = &clusterreconcile.StaticNodeController{
			Store:         option.Store,
			RunnerFactory: option.RunnerFactory,
			Reconciler:    option.Reconciler,
		}
	}

	c := &StaticNodeController{}
	c.syncHandler = func(node *v1.StaticNode) error {
		return reconcileController.Reconcile(context.Background(), node)
	}

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

	return c.syncHandler(node)
}
