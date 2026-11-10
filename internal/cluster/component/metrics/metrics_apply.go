package metrics

import (
	"context"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/component/metrics/manifests"
	"github.com/neutree-ai/neutree/internal/deploy"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// GetMetricsResources returns all Kubernetes resources for the metrics component.
func (m *MetricsComponent) GetMetricsResources(ctx context.Context) (*unstructured.UnstructuredList, error) {
	variables := m.buildManifestVariables()
	variables.EnableVMAgent = util.IsHTTPOrHTTPSURL(m.metricsRemoteWriteURL)

	enableKubeStateMetrics, err := m.supportsKubeStateMetrics()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to check kube-state-metrics support for cluster %s", m.cluster.Metadata.Name)
	}

	enableManagedMetricsExporters, err := m.supportsManagedMetricsExporters()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to check managed metrics exporter support for cluster %s", m.cluster.Metadata.Name)
	}

	variables.EnableKubeStateMetrics = enableKubeStateMetrics
	variables.EnableNodeExporter = enableManagedMetricsExporters
	variables.EnableNeutreeNodeAgentMetrics = enableManagedMetricsExporters

	acceleratorPlans := []metricsAcceleratorPlan(nil)
	acceleratorExporterTargets := []metricsconfig.Target(nil)
	virtualizationTargets := []metricsconfig.Target(nil)
	var nodeAgents []nodeAgentDeploymentPlan
	virtualizationEnabled := m.cluster.Spec.AcceleratorVirtualizationEnabled()
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	if enableManagedMetricsExporters || virtualizationEnabled || external {
		acceleratorPlans, err = m.planAcceleratorExporters(ctx)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to plan accelerator exporters for cluster %s", m.cluster.Metadata.Name)
		}

		acceleratorExporterTargets = m.acceleratorExporterScrapeTargets(acceleratorPlans)

		if virtualizationEnabled {
			virtualizationTargets = m.virtualizationMetricsScrapeTargets(acceleratorPlans)
		}

		if enableManagedMetricsExporters && !external {
			variables.AcceleratorExporters = managedAcceleratorExporters(acceleratorPlans)
		}
	}

	if variables.EnableNeutreeNodeAgentMetrics {
		variables.ScrapeTargets = append(variables.ScrapeTargets, m.nodeAgentScrapeTargets(variables.NeutreeNodeAgentMetricsName)...)
	}

	variables.ScrapeTargets = append(variables.ScrapeTargets, acceleratorExporterTargets...)
	variables.ScrapeTargets = append(variables.ScrapeTargets, virtualizationTargets...)

	if m.metricsTargets != nil {
		snapshot, err := m.metricsTargets.Snapshot()
		if err != nil {
			return nil, errors.Wrap(err, "read registered scrape targets")
		}

		variables.ScrapeTargets = append(variables.ScrapeTargets, snapshot.Targets...)
	}

	if variables.EnableNeutreeNodeAgentMetrics {
		nodeAgents, err = planMetricsNodeAgents(m.cluster.GetVersion(), acceleratorPlans)
		if err != nil {
			return nil, errors.Wrapf(err, "build node agent runtime for cluster %s", m.cluster.Metadata.Name)
		}

		variables = m.nodeAgentManifestVariables(variables, nodeAgents[0])
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

	for index, plan := range nodeAgents {
		if index == 0 {
			continue
		}

		agentResources, err := util.RenderKubernetesManifest(manifests.NodeAgentTemplate, m.nodeAgentManifestVariables(variables, plan))
		if err != nil {
			return nil, errors.Wrapf(err, "failed to render node agent %s", plan.Name)
		}

		objs.Items = append(objs.Items, agentResources.Items...)
	}

	return objs, nil
}

func (m *MetricsComponent) nodeAgentManifestVariables(variables MetricsManifestVariables, plan nodeAgentDeploymentPlan) MetricsManifestVariables {
	variables.NeutreeNodeAgentMetricsName = plan.Name
	variables.NodeAgent = plan.NodeAgent
	variables.NodeAgent.InitContainers = rewriteInitContainerImages(plan.NodeAgent.InitContainers, m.imagePrefix)

	if plan.NodeAgent.Image != "" {
		variables.NeutreeNodeAgentMetricsImage = util.RewriteImageRef(m.imagePrefix, plan.NodeAgent.Image)
	}

	return variables
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
