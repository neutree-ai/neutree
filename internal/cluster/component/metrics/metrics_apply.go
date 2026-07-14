package metrics

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component.
func (m *MetricsComponent) GetMetricsResources(ctx context.Context) (*unstructured.UnstructuredList, error) {
	variables := m.buildManifestVariables()
	enableMetricsPipeline := util.IsHTTPOrHTTPSURL(m.metricsRemoteWriteURL)

	enableKubeStateMetrics, err := m.supportsKubeStateMetrics()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to check kube-state-metrics support for cluster %s", m.cluster.Metadata.Name)
	}
	// HAMi monitor scraping depends on virtualization being enabled. The
	// kube-state-metrics sidecar is a cluster-version capability and is not
	// HAMi-specific.
	variables.EnableVMAgent = enableMetricsPipeline
	variables.EnableHAMiMonitorScrape = enableMetricsPipeline && m.cluster.Spec.AcceleratorVirtualizationEnabled()
	variables.EnableKubeStateMetrics = enableMetricsPipeline && enableKubeStateMetrics

	enableManagedMetricsExporters, err := m.supportsManagedMetricsExporters()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to check managed metrics exporter support for cluster %s", m.cluster.Metadata.Name)
	}

	variables.EnableNodeExporter = enableManagedMetricsExporters
	variables.EnableNeutreeNodeAgentMetrics = enableManagedMetricsExporters
	variables.EnableExternalDCGMScrape = enableMetricsPipeline && m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	acceleratorExporters, err := m.planAcceleratorExporters(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to plan accelerator exporters for cluster %s", m.cluster.Metadata.Name)
	}

	variables.AcceleratorExporters = acceleratorExporters
	variables.NeutreeNodeAgentMetricsEnv = nodeAgentEnvFromAcceleratorExporters(acceleratorExporters)

	if !variables.EnableVMAgent && !variables.EnableNeutreeNodeAgentMetrics {
		return &unstructured.UnstructuredList{}, nil
	}

	vmagentConfig, err := renderKubernetesVMAgentConfig(variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render vmagent config for cluster %s", m.cluster.Metadata.Name)
	}

	variables.VMAgentConfig = vmagentConfig

	objs, err := util.RenderKubernetesManifest(buildMetricsManifestTemplate(variables), variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render metrics manifest for cluster %s", m.cluster.Metadata.Name)
	}

	return objs, nil
}

// ApplyResources applies all metrics resources to the cluster
func (m *MetricsComponent) ApplyResources(ctx context.Context) error {
	objs, err := m.GetMetricsResources(ctx)
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
			v1.NeutreeClusterLabelKey:          m.cluster.Metadata.Name,
			v1.NeutreeClusterWorkspaceLabelKey: m.cluster.Metadata.Workspace,
			v1.LabelManagedBy:                  v1.LabelManagedByValue,
			v1.NeutreeServingVersionLabel:      m.cluster.Spec.Version,
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
