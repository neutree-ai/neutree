package neutreemetrics

import (
	"context"
	"fmt"
	"net/url"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

const (
	MetricsModeManaged  = "managed"
	MetricsModeExternal = "external"

	defaultMetricsPath = "/metrics"

	managedNodeExporterPort          = 19100
	externalNodeExporterPort         = 9100
	managedAcceleratorExporterPort   = 19400
	externalAcceleratorExporterPort  = 9400
	ManagedAcceleratorExporterLabel  = "neutree.ai/metrics-target"
	ManagedAcceleratorExporterValue  = "accelerator-exporter"
	managedNodeExporterApp           = "neutree-node-exporter"
	managedAcceleratorExporterSuffix = "-dcgm-exporter"
	externalNodeExporterApp          = "node-exporter"
	externalAcceleratorExporterApp   = "nvidia-dcgm-exporter"
)

type ScrapeTarget struct {
	TargetType string
	URL        string
}

type ScrapeTargetProvider interface {
	Targets(ctx context.Context, targetType string) ([]ScrapeTarget, error)
}

type StaticScrapeTargetProvider struct {
	MetricsMode string
}

func (p StaticScrapeTargetProvider) Targets(_ context.Context, targetType string) ([]ScrapeTarget, error) {
	port, ok := targetPort(p.metricsMode(), targetType)
	if !ok {
		return nil, nil
	}

	return scrapeTargets(targetType, "127.0.0.1", port, schemesForMetricsMode(p.metricsMode())), nil
}

func (p StaticScrapeTargetProvider) metricsMode() string {
	return normalizeMetricsMode(p.MetricsMode)
}

type KubernetesScrapeTargetProvider struct {
	Client      client.Client
	MetricsMode string
	NodeName    string
}

func (p KubernetesScrapeTargetProvider) Targets(ctx context.Context, targetType string) ([]ScrapeTarget, error) {
	if p.Client == nil || p.NodeName == "" {
		return nil, nil
	}

	mode := normalizeMetricsMode(p.MetricsMode)
	port, ok := targetPort(mode, targetType)

	if !ok {
		return nil, nil
	}

	pods := &corev1.PodList{}
	if err := p.Client.List(ctx, pods, client.MatchingFields{"spec.nodeName": p.NodeName}); err != nil {
		return nil, fmt.Errorf("list scrape target pods: %w", err)
	}

	hosts := make([]string, 0)
	seen := map[string]struct{}{}

	for _, pod := range pods.Items {
		if pod.Spec.NodeName != p.NodeName || pod.Status.PodIP == "" {
			continue
		}

		if !matchesTargetPod(mode, targetType, pod.Labels) {
			continue
		}

		if _, exists := seen[pod.Status.PodIP]; exists {
			continue
		}

		seen[pod.Status.PodIP] = struct{}{}

		hosts = append(hosts, pod.Status.PodIP)
	}

	sort.Strings(hosts)

	result := make([]ScrapeTarget, 0, len(hosts))
	for _, host := range hosts {
		result = append(result, scrapeTargets(targetType, host, port, schemesForMetricsMode(mode))...)
	}

	return result, nil
}

func targetPort(metricsMode string, targetType string) (int, bool) {
	switch targetType {
	case metricsnormalizer.TargetNodeExporter:
		if metricsMode == MetricsModeExternal {
			return externalNodeExporterPort, true
		}

		return managedNodeExporterPort, true
	case metricsnormalizer.TargetAcceleratorExporter:
		if metricsMode == MetricsModeExternal {
			return externalAcceleratorExporterPort, true
		}

		return managedAcceleratorExporterPort, true
	default:
		return 0, false
	}
}

func scrapeTargets(targetType string, host string, port int, schemes []string) []ScrapeTarget {
	result := make([]ScrapeTarget, 0, len(schemes))
	for _, scheme := range schemes {
		result = append(result, ScrapeTarget{
			TargetType: targetType,
			URL: (&url.URL{
				Scheme: scheme,
				Host:   fmt.Sprintf("%s:%d", host, port),
				Path:   defaultMetricsPath,
			}).String(),
		})
	}

	return result
}

func schemesForMetricsMode(metricsMode string) []string {
	if metricsMode == MetricsModeExternal {
		return []string{"http", "https"}
	}

	return []string{"http"}
}

func normalizeMetricsMode(metricsMode string) string {
	if metricsMode == MetricsModeExternal {
		return MetricsModeExternal
	}

	return MetricsModeManaged
}

func matchesTargetPod(metricsMode string, targetType string, labels map[string]string) bool {
	switch targetType {
	case metricsnormalizer.TargetNodeExporter:
		return matchesNodeExporterPod(metricsMode, labels)
	case metricsnormalizer.TargetAcceleratorExporter:
		return matchesAcceleratorExporterPod(metricsMode, labels)
	default:
		return false
	}
}

func matchesNodeExporterPod(metricsMode string, labels map[string]string) bool {
	if metricsMode == MetricsModeExternal {
		return labels["app"] == externalNodeExporterApp ||
			labels["app.kubernetes.io/name"] == externalNodeExporterApp
	}

	return labels["app"] == managedNodeExporterApp
}

func matchesAcceleratorExporterPod(metricsMode string, labels map[string]string) bool {
	app := labels["app"]
	if metricsMode == MetricsModeExternal {
		return app == externalAcceleratorExporterApp
	}

	if labels[ManagedAcceleratorExporterLabel] == ManagedAcceleratorExporterValue {
		return true
	}

	return app != "" && hasSuffix(app, managedAcceleratorExporterSuffix)
}

func hasSuffix(value string, suffix string) bool {
	if len(value) < len(suffix) {
		return false
	}

	return value[len(value)-len(suffix):] == suffix
}
