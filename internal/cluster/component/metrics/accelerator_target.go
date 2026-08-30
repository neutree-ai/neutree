package metrics

import (
	"sort"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// metricsScrapeTarget is the common resolved endpoint rendered into VMagent
// and, for accelerator exporters, passed to NodeAgent. The caller determines
// whether the target represents a managed exporter, an external exporter, or
// a virtualization monitor.
type metricsScrapeTarget struct {
	AcceleratorType string
	Namespace       string
	PodSelector     map[string]string
	Port            int
	MetricsPath     string
}

func (t metricsScrapeTarget) JobName() string {
	return acceleratorExporterJobName(t.AcceleratorType)
}

func (t metricsScrapeTarget) HasCustomMetricsPath() bool {
	return metricsTargetPath(t.MetricsPath) != defaultMetricsPath
}

func (t metricsScrapeTarget) KubernetesLabelSelector() string {
	return labelSelectorString(t.PodSelector)
}

func (m *MetricsComponent) acceleratorExporterScrapeTargets(
	plans []metricsAcceleratorPlan,
	external bool,
) []metricsScrapeTarget {
	targets := make([]metricsScrapeTarget, 0, len(plans))

	for _, plan := range plans {
		target := metricsScrapeTarget{AcceleratorType: plan.AcceleratorType}

		if external {
			if plan.ExternalMetricsTarget == nil {
				continue
			}

			target.Namespace = plan.ExternalMetricsTarget.Namespace
			target.PodSelector = cloneStringMap(plan.ExternalMetricsTarget.PodSelector)
			target.Port = plan.ExternalMetricsTarget.Port
			target.MetricsPath = metricsTargetPath(plan.ExternalMetricsTarget.MetricsPath)
		} else {
			if plan.Exporter == nil {
				continue
			}

			target.Namespace = m.namespace
			target.PodSelector = managedAcceleratorExporterSelector(plan.AcceleratorType)
			target.Port = plan.Exporter.Port
			target.MetricsPath = plan.Exporter.MetricsPath
		}

		targets = append(targets, target)
	}

	return targets
}

func virtualizationMetricsScrapeTargets(plans []metricsAcceleratorPlan) []metricsScrapeTarget {
	targets := make([]metricsScrapeTarget, 0, len(plans))

	for _, plan := range plans {
		if plan.VirtualizationMetricsTarget == nil {
			continue
		}

		targets = append(targets, metricsScrapeTarget{
			Namespace:       plan.VirtualizationMetricsTarget.Namespace,
			PodSelector:     cloneStringMap(plan.VirtualizationMetricsTarget.PodSelector),
			Port:            plan.VirtualizationMetricsTarget.Port,
			MetricsPath:     metricsTargetPath(plan.VirtualizationMetricsTarget.MetricsPath),
			AcceleratorType: plan.AcceleratorType,
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

func labelSelectorString(selector map[string]string) string {
	keys := make([]string, 0, len(selector))

	for key := range selector {
		keys = append(keys, key)
	}

	sort.Strings(keys)
	parts := make([]string, 0, len(keys))

	for _, key := range keys {
		parts = append(parts, key+"="+selector[key])
	}

	return strings.Join(parts, ",")
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
