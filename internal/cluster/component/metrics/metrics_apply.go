package metrics

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component
func (m *MetricsComponent) GetMetricsResources() (*unstructured.UnstructuredList, error) {
	variables := m.buildManifestVariables()

	objs, err := util.RenderKubernetesManifest(metricsManifestTemplate, variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render metrics manifest for cluster %s", m.cluster.Metadata.Name)
	}

	return objs, nil
}

// ApplyResources applies all metrics resources to the cluster
func (m *MetricsComponent) ApplyResources(ctx context.Context) error {
	objs, err := m.GetMetricsResources()
	if err != nil {
		return errors.Wrapf(err, "failed to get metrics resources for cluster %s", m.cluster.Metadata.Name)
	}

	applier := deploy.NewKubernetesDeployer(
		m.ctrlClient,
		m.namespace,
		m.cluster.Metadata.Name, // resourceName
		"metrics",               // componentName
	).
		WithNewObjects(objs).
		WithLabels(map[string]string{
			"cluster":         m.cluster.Metadata.Name,
			"workspace":       m.cluster.Metadata.Workspace,
			v1.LabelManagedBy: v1.LabelManagedByValue,
		}).
		WithLogger(m.logger)

	changedCount, err := applier.Apply(ctx)
	if err != nil {
		return errors.Wrap(err, "failed to apply metrics")
	}

	if changedCount > 0 {
		m.logger.Info("Applied metrics manifests", "changedObjects", changedCount)
	}

	return nil
}

// DeleteResources deletes all metrics resources from the cluster
func (m *MetricsComponent) DeleteResources(ctx context.Context) (bool, error) {
	applier := deploy.NewKubernetesDeployer(
		m.ctrlClient,
		m.namespace,
		m.cluster.Metadata.Name,
		"metrics",
	).WithLogger(m.logger)

	deleted, err := applier.Delete(ctx)
	if err != nil {
		return false, errors.Wrap(err, "failed to delete metrics")
	}

	if deleted {
		m.logger.Info("Deleted all metrics resources")
	}

	return deleted, nil
}
