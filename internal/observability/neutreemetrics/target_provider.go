package neutreemetrics

import (
	"context"
	"fmt"
	"net/url"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

const (
	defaultMetricsPath      = "/metrics"
	managedNodeExporterPort = 19100
	managedNodeExporterApp  = "neutree-node-exporter"

	// AcceleratorExporterTargetLabel and AcceleratorExporterTypeLabel are the
	// orchestration-owned discovery contract for every accelerator exporter.
	// Managed and external workloads use the same labels; the NodeAgent never
	// infers a vendor-specific legacy target.
	AcceleratorExporterTargetLabel = "neutree.ai/metrics-target"
	AcceleratorExporterTargetValue = "accelerator-exporter"
	AcceleratorExporterTypeLabel   = "neutree.ai/accelerator-type"
)

type ScrapeTarget struct {
	TargetType string
	URL        string
}

type ScrapeTargetProvider interface {
	Targets(ctx context.Context, targetType string) ([]ScrapeTarget, error)
}

type StaticScrapeTargetProvider struct {
	AcceleratorType                string
	AcceleratorExporterPort        int
	AcceleratorExporterMetricsPath string
}

func (p StaticScrapeTargetProvider) Targets(_ context.Context, targetType string) ([]ScrapeTarget, error) {
	port, ok := p.targetPort(targetType)
	if !ok {
		return nil, nil
	}

	return scrapeTargetsWithPath(
		targetType,
		"127.0.0.1",
		port,
		[]string{"http"},
		p.metricsPath(targetType),
	), nil
}

func (p StaticScrapeTargetProvider) targetPort(targetType string) (int, bool) {
	switch targetType {
	case metricsnormalizer.TargetNodeExporter:
		return managedNodeExporterPort, true
	case metricsnormalizer.TargetAcceleratorExporter:
		if p.AcceleratorType != "" {
			return p.AcceleratorExporterPort, true
		}
	}

	return 0, false
}

func (p StaticScrapeTargetProvider) metricsPath(targetType string) string {
	if targetType == metricsnormalizer.TargetAcceleratorExporter && p.AcceleratorType != "" {
		return normalizedTargetMetricsPath(p.AcceleratorExporterMetricsPath)
	}

	return defaultMetricsPath
}

type KubernetesScrapeTargetProvider struct {
	Client                         client.Client
	NodeName                       string
	AcceleratorType                string
	AcceleratorExporterPort        int
	AcceleratorExporterMetricsPath string
}

func (p KubernetesScrapeTargetProvider) Targets(ctx context.Context, targetType string) ([]ScrapeTarget, error) {
	if p.Client == nil || p.NodeName == "" {
		return nil, nil
	}

	port, ok := p.targetPort(targetType)
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

		if !p.matchesTargetPod(targetType, pod.Labels) {
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
		result = append(result, scrapeTargetsWithPath(
			targetType,
			host,
			port,
			[]string{"http"},
			p.metricsPath(targetType),
		)...)
	}

	return result, nil
}

func (p KubernetesScrapeTargetProvider) targetPort(targetType string) (int, bool) {
	switch targetType {
	case metricsnormalizer.TargetNodeExporter:
		return managedNodeExporterPort, true
	case metricsnormalizer.TargetAcceleratorExporter:
		if p.AcceleratorType != "" {
			return p.AcceleratorExporterPort, true
		}
	}

	return 0, false
}

func (p KubernetesScrapeTargetProvider) metricsPath(targetType string) string {
	if targetType == metricsnormalizer.TargetAcceleratorExporter && p.AcceleratorType != "" {
		return normalizedTargetMetricsPath(p.AcceleratorExporterMetricsPath)
	}

	return defaultMetricsPath
}

func (p KubernetesScrapeTargetProvider) matchesTargetPod(targetType string, labels map[string]string) bool {
	switch targetType {
	case metricsnormalizer.TargetNodeExporter:
		return labels["app"] == managedNodeExporterApp
	case metricsnormalizer.TargetAcceleratorExporter:
		return p.AcceleratorType != "" &&
			labels[AcceleratorExporterTargetLabel] == AcceleratorExporterTargetValue &&
			labels[AcceleratorExporterTypeLabel] == p.AcceleratorType
	default:
		return false
	}
}

func scrapeTargetsWithPath(targetType string, host string, port int, schemes []string, metricsPath string) []ScrapeTarget {
	result := make([]ScrapeTarget, 0, len(schemes))
	for _, scheme := range schemes {
		result = append(result, ScrapeTarget{
			TargetType: targetType,
			URL: (&url.URL{
				Scheme: scheme,
				Host:   fmt.Sprintf("%s:%d", host, port),
				Path:   normalizedTargetMetricsPath(metricsPath),
			}).String(),
		})
	}

	return result
}

func normalizedTargetMetricsPath(metricsPath string) string {
	metricsPath = strings.TrimSpace(metricsPath)
	if metricsPath == "" {
		return defaultMetricsPath
	}

	if !strings.HasPrefix(metricsPath, "/") {
		return "/" + metricsPath
	}

	return metricsPath
}
