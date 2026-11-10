package metrics

import (
	"context"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

// metricsDeploymentPlan contains only resources owned by Metrics. The excluded
// accelerators were selected by the cluster's Operator dispatch, not readiness.
type metricsDeploymentPlan struct {
	VMAgent               bool
	KubeStateMetrics      bool
	NodeExporter          bool
	LegacyNodeAgent       string
	Accelerators          []metricsAcceleratorPlan
	Exporters             []metricsAcceleratorExporter
	ExporterTargets       []metricsconfig.Target
	VirtualizationTargets []metricsconfig.Target
}

func (m *MetricsComponent) legacyNodeAgentName(plans []metricsAcceleratorPlan) string {
	if len(m.excludedAccelerators) == 0 {
		return neutreeNodeAgentMetricsName
	}

	if len(plans) == 0 {
		return ""
	}

	return neutreeNodeAgentMetricsName + "-" + sanitizeKubernetesNameValue(plans[0].AcceleratorType)
}

func (m *MetricsComponent) deploymentPlan(ctx context.Context) (metricsDeploymentPlan, error) {
	plan := metricsDeploymentPlan{VMAgent: util.IsHTTPOrHTTPSURL(m.metricsRemoteWriteURL)}
	var err error

	plan.KubeStateMetrics, err = m.supportsKubeStateMetrics()
	if err != nil {
		return plan, err
	}

	plan.NodeExporter, err = m.supportsManagedMetricsExporters()
	if err != nil {
		return plan, err
	}

	managed := plan.NodeExporter

	if m.acceleratorOnly != "" {
		plan.VMAgent = false
		plan.KubeStateMetrics = false
		plan.NodeExporter = false
	}

	virtualization := m.cluster.Spec.AcceleratorVirtualizationEnabled()
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	if managed || virtualization || external || m.acceleratorOnly != "" {
		plan.Accelerators, err = m.planAcceleratorExporters(ctx)
		if err != nil {
			return plan, err
		}

		plan.ExporterTargets = m.acceleratorExporterScrapeTargets(plan.Accelerators)
		if virtualization {
			plan.VirtualizationTargets = m.virtualizationMetricsScrapeTargets(plan.Accelerators)
		}

		if (managed || m.acceleratorOnly != "") && !external {
			plan.Exporters = managedAcceleratorExporters(plan.Accelerators)
		}
	}

	if managed || m.acceleratorOnly != "" {
		plan.LegacyNodeAgent = m.legacyNodeAgentName(plan.Accelerators)
	}

	return plan, nil
}

func (p metricsDeploymentPlan) nodeAgentNames() []string {
	var names []string
	if p.LegacyNodeAgent != "" {
		names = append(names, p.LegacyNodeAgent)
	}

	return names
}
