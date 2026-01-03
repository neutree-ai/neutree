package router

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetRouteResources returns all Kubernetes resources for the route component
func (r *RouterComponent) GetRouteResources() (*unstructured.UnstructuredList, error) {
	variables := r.buildManifestVariables()

	objs, err := util.RenderKubernetesManifest(routerMainifestTemplate, variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render router manifest for cluster %s", r.cluster.Metadata.Name)
	}

	return objs, nil
}

// ApplyResources applies all route resources to the cluster
func (r *RouterComponent) ApplyResources(ctx context.Context) error {
	objs, err := r.GetRouteResources()
	if err != nil {
		return errors.Wrapf(err, "failed to get route resources for cluster %s", r.cluster.Metadata.Name)
	}

	applier := deploy.NewKubernetesDeployer(
		r.ctrlClient,
		r.namespace,
		r.cluster.Metadata.Name, // resourceName
		"router",                // componentName
	).
		WithNewObjects(objs).
		WithLabels(map[string]string{
			v1.NeutreeClusterLabelKey:          r.cluster.Metadata.Name,
			v1.NeutreeClusterWorkspaceLabelKey: r.cluster.Metadata.Workspace,
			v1.LabelManagedBy:                  v1.LabelManagedByValue,
		}).
		WithLogger(r.logger)

	changedCount, err := applier.Apply(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply router")
	}

	if changedCount > 0 {
		r.logger.Info("Applied router manifests", "changedObjects", changedCount)
	}

	return nil
}

// DeleteResources deletes all route resources from the cluster
func (r *RouterComponent) DeleteResources(ctx context.Context) (bool, error) {
	applier := deploy.NewKubernetesDeployer(
		r.ctrlClient,
		r.namespace,
		r.cluster.Metadata.Name,
		"router",
	).WithLogger(r.logger)

	deleted, err := applier.Delete(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to delete router")
	}

	if deleted {
		r.logger.Info("Deleted all router resources")
	}

	return deleted, nil
}
