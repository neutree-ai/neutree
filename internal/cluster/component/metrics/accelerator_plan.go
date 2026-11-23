package metrics

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

// metricsAcceleratorPlan keeps profile-level decisions separate from a
// managed exporter workload. External mode can therefore select a target and
// NodeAgent runtime without inventing a DaemonSet.
type metricsAcceleratorPlan struct {
	AcceleratorType             string
	OperatorManaged             bool
	Exporter                    *metricsAcceleratorExporter
	NodeAgentRuntime            *v1.NodeAgentRuntimeProfile
	VirtualizationMetricsTarget *v1.MetricsTargetProfile
	ExporterTarget              *v1.MetricsTargetProfile
}

func (m *MetricsComponent) planAcceleratorExporters(ctx context.Context) ([]metricsAcceleratorPlan, error) {
	if m.acceleratorMgr == nil {
		return nil, nil
	}

	acceleratorTypes := append([]string{}, m.acceleratorMgr.SupportPlugins()...)
	sort.Strings(acceleratorTypes)

	candidates := make([]metricsAcceleratorPlan, 0, len(acceleratorTypes))

	for _, acceleratorType := range acceleratorTypes {
		plan, ok := m.buildAcceleratorPlan(ctx, acceleratorType)

		if ok {
			candidates = append(candidates, plan)
		}
	}

	return m.selectClusterAcceleratorPlan(ctx, candidates)
}

func (m *MetricsComponent) buildAcceleratorPlan(
	ctx context.Context,
	acceleratorType string,
) (metricsAcceleratorPlan, bool) {
	profile, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, publicaccelerator.AcceleratorProfileInput{AcceleratorType: acceleratorType, Cluster: m.cluster})
	if err != nil {
		klog.Warningf("Skipping accelerator metrics plan for %s: %v", acceleratorType, err)

		return metricsAcceleratorPlan{}, false
	}

	if profile == nil {
		return metricsAcceleratorPlan{}, false
	}

	_, operatorManaged := m.acceleratorOperators[acceleratorType]

	plan := metricsAcceleratorPlan{
		AcceleratorType:             acceleratorType,
		OperatorManaged:             operatorManaged,
		NodeAgentRuntime:            profile.NodeAgentRuntime,
		VirtualizationMetricsTarget: resolveVirtualizationMetricsTargetNamespace(profile.VirtualizationMetricsTarget, m.namespace),
	}
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	if external {
		plan.ExporterTarget = cloneMetricsTarget(profile.ExternalMetricsTarget)
		if plan.ExporterTarget != nil {
			plan.ExporterTarget.MetricsPath = metricsTargetPath(plan.ExporterTarget.MetricsPath)
		}
	}

	if profile.MetricsExporter == nil {
		if !external || plan.ExporterTarget == nil {
			return metricsAcceleratorPlan{}, false
		}

		return plan, true
	}

	plan.Exporter = buildManagedAcceleratorExporter(
		acceleratorType,
		profile.MetricsExporter,
		m.imagePrefix,
	)

	if !external {
		plan.ExporterTarget = &v1.MetricsTargetProfile{
			Namespace: m.namespace, PodSelector: managedAcceleratorExporterSelector(acceleratorType),
			Port: plan.Exporter.Port, MetricsPath: plan.Exporter.MetricsPath,
		}
	}

	return plan, true
}

func (m *MetricsComponent) selectClusterAcceleratorPlan(
	ctx context.Context,
	candidates []metricsAcceleratorPlan,
) ([]metricsAcceleratorPlan, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	var nodes []corev1.Node
	nodesLoaded := false
	var matching []metricsAcceleratorPlan
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	for _, plan := range candidates {
		if external && plan.ExporterTarget == nil {
			continue
		}

		// Operator plans already matched hardware through MatchCluster; selectors only gate scheduling.
		if !plan.OperatorManaged {
			if plan.Exporter == nil {
				continue
			}

			if external && len(plan.Exporter.NodeSelector) == 0 {
				continue
			}

			if !nodesLoaded {
				var err error

				nodes, err = m.clusterNodes(ctx)
				if err != nil {
					return nil, err
				}

				nodesLoaded = true
			}

			if len(plan.Exporter.NodeSelector) > 0 &&
				!acceleratorExporterMatchesAnyNode(*plan.Exporter, nodes) {
				continue
			}
		}

		matching = append(matching, plan)
		if len(matching) > 1 {
			return nil, fmt.Errorf("currently supports only one matching accelerator exporter")
		}
	}

	return matching, nil
}

func (m *MetricsComponent) clusterNodes(ctx context.Context) ([]corev1.Node, error) {
	if m.ctrlClient == nil {
		return nil, fmt.Errorf("kubernetes client is required to match accelerator exporter node selectors")
	}

	nodeList := &corev1.NodeList{}
	if err := m.ctrlClient.List(ctx, nodeList); err != nil {
		return nil, fmt.Errorf("list cluster nodes: %w", err)
	}

	return nodeList.Items, nil
}

func acceleratorExporterMatchesAnyNode(exporter metricsAcceleratorExporter, nodes []corev1.Node) bool {
	if len(exporter.NodeSelector) == 0 {
		return false
	}

	for _, node := range nodes {
		if nodeMatchesSelector(node, exporter.NodeSelector) {
			return true
		}
	}

	return false
}

func nodeMatchesSelector(node corev1.Node, selector map[string]string) bool {
	for key, value := range selector {
		if node.Labels[key] != value {
			return false
		}
	}

	return true
}
