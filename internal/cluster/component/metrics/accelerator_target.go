package metrics

import (
	"strings"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/metricsconfig"
)

func (m *MetricsComponent) acceleratorExporterScrapeTargets(plans []metricsAcceleratorPlan) []metricsconfig.Target {
	targets := make([]metricsconfig.Target, 0, len(plans))

	for _, plan := range plans {
		endpoint := plan.ExporterTarget
		if endpoint == nil {
			continue
		}

		acceleratorType := plan.AcceleratorType
		rules := m.podScrapeRelabelConfigs(endpoint.Port, "namespace", "pod")
		rules = append(rules, promrelabel.RelabelConfig{TargetLabel: "accelerator_type", Replacement: &acceleratorType})
		targets = append(targets, metricsconfig.Target{
			JobName: acceleratorExporterJobName(acceleratorType), Namespace: endpoint.Namespace,
			Selectors: metricsconfig.PodSelectors(endpoint.PodSelector), Port: endpoint.Port, MetricsPath: endpoint.MetricsPath,
			RelabelConfigs: rules,
		})
	}

	return targets
}

func (m *MetricsComponent) virtualizationMetricsScrapeTargets(plans []metricsAcceleratorPlan) []metricsconfig.Target {
	targets := make([]metricsconfig.Target, 0, len(plans))

	for _, plan := range plans {
		endpoint := plan.VirtualizationMetricsTarget
		if endpoint == nil {
			continue
		}

		targets = append(targets, metricsconfig.Target{
			JobName: "hami-vgpu-monitor", Namespace: endpoint.Namespace, Selectors: metricsconfig.PodSelectors(endpoint.PodSelector),
			Port: endpoint.Port, MetricsPath: metricsTargetPath(endpoint.MetricsPath),
			RelabelConfigs: m.podScrapeRelabelConfigs(endpoint.Port, "monitor_namespace", "monitor_pod"),
		})
	}

	return targets
}

func cloneMetricsTarget(target *v1.MetricsTargetProfile) *v1.MetricsTargetProfile {
	if target == nil {
		return nil
	}

	copy := *target
	copy.PodSelector = cloneStringMap(target.PodSelector)

	return &copy
}

func resolveVirtualizationMetricsTargetNamespace(
	profile *v1.MetricsTargetProfile,
	namespace string,
) *v1.MetricsTargetProfile {
	target := cloneMetricsTarget(profile)
	if target == nil || target.Namespace != "" || namespace == "" {
		return target
	}

	target.Namespace = namespace

	return target
}

func cloneStringMap(values map[string]string) map[string]string {
	if len(values) == 0 {
		return nil
	}

	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}

	return copy
}

func metricsTargetPath(metricsPath string) string {
	metricsPath = strings.TrimSpace(metricsPath)
	if metricsPath == "" {
		return defaultMetricsPath
	}

	if !strings.HasPrefix(metricsPath, "/") {
		return "/" + metricsPath
	}

	return metricsPath
}
