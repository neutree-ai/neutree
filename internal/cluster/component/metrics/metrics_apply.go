package metrics

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	acceleratoroperator "github.com/neutree-ai/neutree/internal/accelerator/operator"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics/manifests"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/clustercache"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component.
func (m *MetricsComponent) GetMetricsResources(ctx context.Context) (*unstructured.UnstructuredList, error) {
	plan, err := m.deploymentPlan(ctx)
	if err != nil {
		return nil, errors.Wrapf(err, "plan metrics resources for cluster %s", m.cluster.Metadata.Name)
	}

	variables := m.buildManifestVariables()
	variables.EnableVMAgent = plan.VMAgent
	variables.EnableKubeStateMetrics = plan.KubeStateMetrics
	variables.EnableNodeExporter = plan.NodeExporter
	variables.EnableNeutreeNodeAgentMetrics = plan.LegacyNodeAgent != ""
	variables.NeutreeNodeAgentMetricsName = plan.LegacyNodeAgent
	variables.AcceleratorExporters = plan.Exporters
	variables.ScrapeTargets = append(variables.ScrapeTargets, m.nodeAgentScrapeTargets(plan.LegacyNodeAgent)...)
	variables.ScrapeTargets = append(variables.ScrapeTargets, plan.ExporterTargets...)
	variables.ScrapeTargets = append(variables.ScrapeTargets, plan.VirtualizationTargets...)

	if m.metricsTargets != nil {
		snapshot, err := m.metricsTargets.Snapshot()
		if err != nil {
			return nil, errors.Wrap(err, "read registered scrape targets")
		}

		variables.ScrapeTargets = append(variables.ScrapeTargets, snapshot.Targets...)
	}

	if variables.EnableNeutreeNodeAgentMetrics {
		nodeAgent, err := selectedMetricsNodeAgent(m.cluster.GetVersion(), plan.Accelerators)
		if err != nil {
			return nil, errors.Wrapf(err, "build node agent runtime for cluster %s", m.cluster.Metadata.Name)
		}

		if len(m.excludedAccelerators) > 0 && len(plan.Accelerators) > 0 && len(nodeAgent.NodeSelector) == 0 {
			if plan.Accelerators[0].Exporter == nil || len(plan.Accelerators[0].Exporter.NodeSelector) == 0 {
				return nil, errors.Errorf("legacy accelerator %s has no dedicated node scope", plan.Accelerators[0].AcceleratorType)
			}

			nodeAgent.NodeSelector = cloneStringMap(plan.Accelerators[0].Exporter.NodeSelector)
		}

		variables.NodeAgent = nodeAgent
		if nodeAgent.Image != "" {
			variables.NeutreeNodeAgentMetricsImage = util.RewriteImageRef(m.imagePrefix, nodeAgent.Image)
		}
	}

	if variables.EnableVMAgent {
		for _, name := range m.cacheProviders.Names() {
			provider, _ := m.cacheProviders.Get(name)
			if metricsProvider, ok := provider.(clustercache.MetricsProvider); ok {
				variables.CacheScrapeConfig += metricsProvider.MetricsScrapeConfig(m.cluster, m.namespace)
			}
		}

		vmagentConfig, err := renderKubernetesVMAgentConfig(variables)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to render vmagent config for cluster %s", m.cluster.Metadata.Name)
		}

		variables.VMAgentConfig = vmagentConfig
	}

	if !variables.EnableVMAgent && !variables.EnableKubeStateMetrics && !variables.EnableNodeExporter &&
		!variables.EnableNeutreeNodeAgentMetrics && len(variables.AcceleratorExporters) == 0 {
		return &unstructured.UnstructuredList{}, nil
	}

	objs, err := util.RenderKubernetesManifest(manifests.Template, variables)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to render metrics manifest for cluster %s", m.cluster.Metadata.Name)
	}

	return objs, nil
}

// GetAcceleratorResources renders only the NodeAgent and accelerator exporter
// resources for an operator-owned accelerator. Generic vmagent, node exporter,
// and kube-state-metrics resources remain owned by MetricsComponent.
func (m *MetricsComponent) GetAcceleratorResources(ctx context.Context, acceleratorType string) (*unstructured.UnstructuredList, error) {
	return m.WithAcceleratorOnly(acceleratorType).GetMetricsResources(ctx)
}

func (m *MetricsComponent) acceleratorExporterMode() v1.ClusterAcceleratorExporterMode {
	if m == nil || m.cluster == nil || m.cluster.Spec == nil {
		return v1.ClusterAcceleratorExporterModeManaged
	}

	return m.cluster.Spec.Config.AcceleratorExporterMode()
}

// ApplyResources applies all metrics resources to the cluster
func (m *MetricsComponent) ApplyResources(ctx context.Context) error {
	objs, err := m.GetMetricsResources(ctx)
	if err != nil {
		return errors.Wrapf(err, "failed to get metrics resources for cluster %s", m.cluster.Metadata.Name)
	}

	labels := m.resourceLabels()
	if err := acceleratoroperator.ReleaseOwnedResources(
		ctx, m.ctrlClient, m.namespace, m.cluster, "metrics", m.acceleratorOperators, m.excludedAccelerators, labels,
	); err != nil {
		return errors.Wrap(err, "failed to transfer operator resource ownership")
	}

	applier := deploy.NewKubernetesDeployer(
		m.ctrlClient,
		m.namespace,
		m.cluster.Metadata.Name, // resourceName
		"metrics",               // componentName
	).
		WithNewObjects(objs).
		WithLabels(labels).
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
	if err := acceleratoroperator.ReleaseOwnedResources(
		ctx, m.ctrlClient, m.namespace, m.cluster, "metrics", m.acceleratorOperators, m.excludedAccelerators, m.resourceLabels(),
	); err != nil {
		return false, errors.Wrap(err, "failed to transfer operator resource ownership")
	}

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

func (m *MetricsComponent) resourceLabels() map[string]string {
	return map[string]string{
		v1.NeutreeClusterLabelKey:          m.cluster.Metadata.Name,
		v1.NeutreeClusterWorkspaceLabelKey: m.cluster.Metadata.Workspace,
		v1.LabelManagedBy:                  v1.LabelManagedByValue,
		v1.NeutreeServingVersionLabel:      m.cluster.Spec.Version,
	}
}
