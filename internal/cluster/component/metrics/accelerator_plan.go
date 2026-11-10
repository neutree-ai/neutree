package metrics

import (
	"context"
	"errors"
	"fmt"
	"maps"
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
		plan, ok, err := m.buildAcceleratorPlan(ctx, acceleratorType)
		if err != nil {
			return nil, err
		}

		if ok {
			candidates = append(candidates, plan)
		}
	}

	return m.selectClusterAcceleratorPlan(ctx, candidates)
}

func (m *MetricsComponent) buildAcceleratorPlan(
	ctx context.Context,
	acceleratorType string,
) (metricsAcceleratorPlan, bool, error) {
	profile, err := m.acceleratorMgr.GetAcceleratorProfile(ctx, publicaccelerator.AcceleratorProfileInput{AcceleratorType: acceleratorType, Cluster: m.cluster})
	if err != nil {
		if errors.Is(err, publicaccelerator.ErrKubernetesProfileResolution) {
			return metricsAcceleratorPlan{}, false, err
		}

		klog.V(4).Infof("skip accelerator metrics exporter for %s: failed to get accelerator profile: %v", acceleratorType, err)

		return metricsAcceleratorPlan{}, false, nil
	}

	if profile == nil {
		return metricsAcceleratorPlan{}, false, nil
	}

	plan := metricsAcceleratorPlan{
		AcceleratorType:             acceleratorType,
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
			return metricsAcceleratorPlan{}, false, nil
		}

		return plan, true, nil
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

	return plan, true, nil
}

func (m *MetricsComponent) selectClusterAcceleratorPlan(
	ctx context.Context,
	candidates []metricsAcceleratorPlan,
) ([]metricsAcceleratorPlan, error) {
	if len(candidates) == 0 {
		return nil, nil
	}

	nodes, err := m.clusterNodes(ctx)
	if err != nil {
		return nil, err
	}

	var matching []metricsAcceleratorPlan
	external := m.acceleratorExporterMode() == v1.ClusterAcceleratorExporterModeExternal

	for _, plan := range candidates {
		if external && plan.ExporterTarget == nil {
			continue
		}

		if plan.Exporter == nil {
			continue
		}

		if external && len(plan.Exporter.NodeSelector) == 0 {
			continue
		}

		if len(plan.Exporter.NodeSelector) > 0 &&
			!acceleratorExporterMatchesAnyNode(*plan.Exporter, nodes) {
			continue
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

	selector := exporter.NodeSelector
	if exporter.AcceleratorType == v1.AcceleratorTypeNVIDIAGPU.String() &&
		selector[publicaccelerator.NvidiaGPUClientLabel] == publicaccelerator.NvidiaGPUDiscoveryLabelValue {
		// The client gate controls scheduling, not hardware discovery during driver recovery.
		selector = maps.Clone(exporter.NodeSelector)
		delete(selector, publicaccelerator.NvidiaGPUClientLabel)
	}

	for _, node := range nodes {
		if nodeMatchesSelector(node, selector) {
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
