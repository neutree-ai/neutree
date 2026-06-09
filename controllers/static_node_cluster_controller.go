package controllers

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	clusterreconcile "github.com/neutree-ai/neutree/internal/cluster"
)

type AcceleratorProfileProvider interface {
	GetAcceleratorProfiles(ctx context.Context, acceleratorTypes []string) (map[string]*v1.AcceleratorProfile, error)
}

type StaticNodeClusterController struct {
	syncHandler func(cluster *v1.StaticNodeCluster) error
}

type StaticNodeClusterControllerOption struct {
	Store                      clusterreconcile.StaticNodeClusterStore
	Reconciler                 *clusterreconcile.StaticNodeClusterReconciler
	AcceleratorProfileProvider AcceleratorProfileProvider
	ReconcileController        *clusterreconcile.StaticNodeClusterController
}

func NewStaticNodeClusterController(option *StaticNodeClusterControllerOption) (*StaticNodeClusterController, error) {
	if option == nil {
		return nil, errors.New("static node cluster controller option is required")
	}

	reconcileController := option.ReconcileController
	if reconcileController == nil {
		reconcileController = &clusterreconcile.StaticNodeClusterController{
			Store:      option.Store,
			Reconciler: option.Reconciler,
		}
	}

	c := &StaticNodeClusterController{}
	c.syncHandler = func(cluster *v1.StaticNodeCluster) error {
		profiles := map[string]*v1.AcceleratorProfile{}

		if option.AcceleratorProfileProvider != nil {
			providerProfiles, err := option.AcceleratorProfileProvider.GetAcceleratorProfiles(
				context.Background(),
				staticNodeClusterAcceleratorTypes(cluster),
			)
			if err != nil {
				return errors.Wrap(err, "failed to get accelerator profiles")
			}

			profiles = providerProfiles
		}

		return reconcileController.Reconcile(context.Background(), cluster, profiles)
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

func staticNodeClusterAcceleratorTypes(cluster *v1.StaticNodeCluster) []string {
	if cluster == nil || cluster.Spec == nil {
		return nil
	}

	seen := map[string]struct{}{}
	acceleratorTypes := make([]string, 0, len(cluster.Spec.Nodes))

	for _, node := range cluster.Spec.Nodes {
		if node.AcceleratorType == "" {
			continue
		}

		if _, ok := seen[node.AcceleratorType]; ok {
			continue
		}

		seen[node.AcceleratorType] = struct{}{}
		acceleratorTypes = append(acceleratorTypes, node.AcceleratorType)
	}

	return acceleratorTypes
}
