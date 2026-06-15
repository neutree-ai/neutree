package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
)

type StaticNodeClusterController struct {
	syncHandler func(cluster *v1.StaticNodeCluster) error
}

type StaticNodeClusterControllerOption struct {
	Store                  clusterreconcile.StaticNodeClusterStore
	Reconciler             *clusterreconcile.StaticNodeClusterReconciler
	RuntimeProfileProvider clusterreconcile.RuntimeProfileProvider
	ReconcileController    *clusterreconcile.StaticNodeClusterController
}

func NewStaticNodeClusterController(option *StaticNodeClusterControllerOption) (*StaticNodeClusterController, error) {
	if option == nil {
		return nil, errors.New("static node cluster controller option is required")
	}

	reconcileController := option.ReconcileController
	if reconcileController == nil {
		reconciler := option.Reconciler
		if reconciler == nil {
			reconciler = &clusterreconcile.StaticNodeClusterReconciler{
				RuntimeProfileProvider: option.RuntimeProfileProvider,
			}
		}

		reconcileController = &clusterreconcile.StaticNodeClusterController{
			Store:      option.Store,
			Reconciler: reconciler,
		}
	}

	c := &StaticNodeClusterController{}
	c.syncHandler = func(cluster *v1.StaticNodeCluster) error {
		return reconcileController.Reconcile(context.Background(), cluster)
	}

	return c, nil
}

func (c *StaticNodeClusterController) Reconcile(obj interface{}) error {
	cluster, ok := obj.(*v1.StaticNodeCluster)
	if !ok {
		return errors.New("failed to assert obj to *v1.StaticNodeCluster")
	}

	if cluster.Metadata != nil {
		klog.V(4).Info("Reconcile static node cluster " + cluster.Metadata.WorkspaceName())
	}

	return c.syncHandler(cluster)
}
