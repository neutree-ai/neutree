package metrics

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// metricsAcceleratorPlan keeps profile-level decisions separate from a
// managed exporter workload. External mode can therefore select a target and
// NodeAgent runtime without inventing a DaemonSet.
type metricsAcceleratorPlan struct {
	AcceleratorType             string
	Exporter                    *metricsAcceleratorExporter
	NodeAgentRuntime            *v1.NodeAgentRuntimeProfile
	VirtualizationMetricsTarget *v1.MetricsTargetProfile
	ExternalMetricsTarget       *v1.MetricsTargetProfile
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
	profile, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, acceleratorType)
	if err != nil {
		klog.V(4).Infof("skip accelerator metrics exporter for %s: failed to get accelerator profile: %v", acceleratorType, err)
		return metricsAcceleratorPlan{}, false
	}

	if profile == nil {
		return metricsAcceleratorPlan{}, false
	}

	plan := metricsAcceleratorPlan{
		AcceleratorType:             acceleratorType,
		NodeAgentRuntime:            profile.NodeAgentRuntime,
		VirtualizationMetricsTarget: resolveVirtualizationMetricsTargetNamespace(profile.VirtualizationMetricsTarget, m.namespace),
		ExternalMetricsTarget:       cloneMetricsTarget(profile.ExternalMetricsTarget),
	}

	if profile.MetricsExporter == nil {
		if m.acceleratorExporterMode() != v1.ClusterAcceleratorExporterModeExternal ||
			plan.ExternalMetricsTarget == nil {
			return metricsAcceleratorPlan{}, false
		}

		return plan, true
	}

	plan.Exporter = buildManagedAcceleratorExporter(
		acceleratorType,
		profile.MetricsExporter,
		m.imagePrefix,
	)

	return plan, true
}

func (m *MetricsComponent) selectClusterAcceleratorPlan(
	ctx context.Context,
	candidates []metricsAcceleratorPlan,
) ([]metricsAcceleratorPlan, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	var matching []metricsAcceleratorPlan
	var nodes []corev1.Node
	nodesLoaded := false
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	for _, plan := range candidates {
		if external {
			// An external target describes how to scrape an exporter, but it does
			// not identify the accelerator type by itself. Require the same
			// profile-owned runtime selector used by managed exporters.
			if plan.ExternalMetricsTarget == nil || plan.Exporter == nil ||
				len(plan.Exporter.NodeSelector) == 0 {
				continue
			}
		} else if plan.Exporter == nil {
			continue
		}

		matches := len(plan.Exporter.NodeSelector) == 0
		if !matches {
			if !nodesLoaded {
				loadedNodes, err := m.clusterNodes(ctx)
				if err != nil {
					return nil, err
				}

				nodes = loadedNodes
				nodesLoaded = true
			}

			matches = acceleratorExporterMatchesAnyNode(*plan.Exporter, nodes)
		}

		if matches {
			matching = append(matching, plan)
			if len(matching) > 1 {
				return nil, fmt.Errorf("currently supports only one matching accelerator exporter")
			}
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
