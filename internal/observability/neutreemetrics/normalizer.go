package neutreemetrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
)

const (
	TargetNodeExporter        = "node-exporter"
	TargetAcceleratorExporter = "accelerator-exporter"

	AcceleratorTypeNvidiaGPU = "nvidia_gpu"
	ExporterKindDCGM         = "dcgm-exporter"
)

type CanonicalLabels struct {
	Workspace     string
	StaticCluster string
	ClusterType   string
	Node          string
	NodeIP        string
	NodeRole      string
}

type ScrapeResult struct {
	Target string
	Up     bool
	Body   string
	Error  string
}

type NormalizeRequest struct {
	Labels              CanonicalLabels
	NodeExporter        ScrapeResult
	AcceleratorExporter *ScrapeResult
	AcceleratorType     string
	ExporterKind        string
}

type Normalizer struct{}

type sample struct {
	name   string
	labels map[string]string
	value  float64
}

type canonicalSample struct {
	name   string
	labels map[string]string
	value  float64
}

func (n *Normalizer) Normalize(req NormalizeRequest) string {
	var samples []canonicalSample

	samples = append(samples, scrapeUpSample(req.Labels, TargetNodeExporter, req.NodeExporter.Up))
	if req.NodeExporter.Up {
		samples = append(samples, normalizeNodeSamples(req.Labels, req.NodeExporter.Body)...)
	}

	if req.AcceleratorExporter != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetAcceleratorExporter, req.AcceleratorExporter.Up))

		if req.AcceleratorExporter.Up {
			acceleratorSamples, supported := normalizeAcceleratorSamples(
				req.Labels,
				req.AcceleratorType,
				req.ExporterKind,
				req.AcceleratorExporter.Body,
			)
			samples = append(samples, acceleratorSamples...)

			if !supported {
				samples = append(samples, mappingSupportedSample(req.Labels, req.AcceleratorType, req.ExporterKind, false))
			}
		}
	}

	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].name == samples[j].name {
			return labelsKey(samples[i].labels) < labelsKey(samples[j].labels)
		}

		return samples[i].name < samples[j].name
	})

	var builder strings.Builder
	for _, s := range samples {
		builder.WriteString(formatSample(s))
		builder.WriteByte('\n')
	}

	return builder.String()
}

func normalizeNodeSamples(labels CanonicalLabels, raw string) []canonicalSample {
	parsed := indexFirstSampleByName(parsePrometheusText(raw))
	var result []canonicalSample

	if total, ok := parsed["node_memory_MemTotal_bytes"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_total_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  total.value,
		})
	}

	if available, ok := parsed["node_memory_MemAvailable_bytes"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_available_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  available.value,
		})
	}

	total, hasTotal := parsed["node_memory_MemTotal_bytes"]
	available, hasAvailable := parsed["node_memory_MemAvailable_bytes"]

	if hasTotal && hasAvailable {
		result = append(result, canonicalSample{
			name:   "neutree_node_memory_used_bytes",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  total.value - available.value,
		})
	}

	if load1, ok := parsed["node_load1"]; ok {
		result = append(result, canonicalSample{
			name:   "neutree_node_load1",
			labels: baseLabels(labels, TargetNodeExporter),
			value:  load1.value,
		})
	}

	return result
}

func normalizeAcceleratorSamples(
	labels CanonicalLabels,
	acceleratorType string,
	exporterKind string,
	raw string,
) ([]canonicalSample, bool) {
	if acceleratorType != AcceleratorTypeNvidiaGPU || exporterKind != ExporterKindDCGM {
		return nil, false
	}

	var result []canonicalSample

	for _, rawSample := range parsePrometheusText(raw) {
		metricLabels := acceleratorLabels(labels, exporterKind, rawSample.labels)

		switch rawSample.name {
		case "DCGM_FI_DEV_GPU_UTIL":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_utilization_ratio",
				labels: metricLabels,
				value:  rawSample.value / 100,
			})
		case "DCGM_FI_DEV_FB_USED":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_used_bytes",
				labels: metricLabels,
				value:  rawSample.value * 1024 * 1024,
			})
		case "DCGM_FI_DEV_FB_TOTAL":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_total_bytes",
				labels: metricLabels,
				value:  rawSample.value * 1024 * 1024,
			})
		}
	}

	return result, true
}

func scrapeUpSample(labels CanonicalLabels, target string, up bool) canonicalSample {
	value := float64(0)
	if up {
		value = 1
	}

	metricLabels := baseLabels(labels, "neutree-metrics")
	metricLabels["target"] = target

	return canonicalSample{
		name:   "neutree_metrics_scrape_up",
		labels: metricLabels,
		value:  value,
	}
}

func mappingSupportedSample(
	labels CanonicalLabels,
	acceleratorType string,
	exporterKind string,
	supported bool,
) canonicalSample {
	value := float64(0)
	if supported {
		value = 1
	}

	metricLabels := baseLabels(labels, "neutree-metrics")
	metricLabels["accelerator_type"] = acceleratorType
	metricLabels["exporter_kind"] = exporterKind

	return canonicalSample{
		name:   "neutree_metrics_mapping_supported",
		labels: metricLabels,
		value:  value,
	}
}

func baseLabels(labels CanonicalLabels, source string) map[string]string {
	clusterType := labels.ClusterType
	if clusterType == "" {
		clusterType = "ray"
	}

	return map[string]string{
		"workspace":      labels.Workspace,
		"static_cluster": labels.StaticCluster,
		"cluster_type":   clusterType,
		"node":           labels.Node,
		"node_ip":        labels.NodeIP,
		"node_role":      labels.NodeRole,
		"source":         source,
	}
}

func acceleratorLabels(labels CanonicalLabels, exporterKind string, rawLabels map[string]string) map[string]string {
	result := baseLabels(labels, TargetAcceleratorExporter)
	result["exporter_kind"] = exporterKind

	if gpu := rawLabels["gpu"]; gpu != "" {
		result["gpu"] = gpu
	}

	if uuid := rawLabels["UUID"]; uuid != "" {
		result["gpu_uuid"] = uuid
	}

	if device := rawLabels["device"]; device != "" {
		result["device"] = device
	}

	if model := rawLabels["modelName"]; model != "" {
		result["gpu_model"] = model
	}

	return result
}

func parsePrometheusText(raw string) []sample {
	var result []sample

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		metricPart, valuePart, ok := strings.Cut(line, " ")
		if !ok {
			continue
		}

		value, err := strconv.ParseFloat(strings.Fields(valuePart)[0], 64)
		if err != nil {
			continue
		}

		name, labels := parseMetricPart(metricPart)
		if name == "" {
			continue
		}

		result = append(result, sample{name: name, labels: labels, value: value})
	}

	return result
}

func parseMetricPart(metricPart string) (string, map[string]string) {
	openIndex := strings.Index(metricPart, "{")
	if openIndex < 0 {
		return metricPart, nil
	}

	closeIndex := strings.LastIndex(metricPart, "}")
	if closeIndex < openIndex {
		return "", nil
	}

	return metricPart[:openIndex], parseLabels(metricPart[openIndex+1 : closeIndex])
}

func parseLabels(raw string) map[string]string {
	labels := map[string]string{}

	for _, item := range splitLabelItems(raw) {
		key, value, ok := strings.Cut(item, "=")
		if !ok {
			continue
		}

		labels[strings.TrimSpace(key)] = strings.Trim(strings.TrimSpace(value), `"`)
	}

	return labels
}

func splitLabelItems(raw string) []string {
	var items []string
	var current strings.Builder
	escaped := false
	inQuote := false

	for _, ch := range raw {
		switch {
		case escaped:
			current.WriteRune(ch)

			escaped = false
		case ch == '\\':
			current.WriteRune(ch)

			escaped = true
		case ch == '"':
			current.WriteRune(ch)

			inQuote = !inQuote
		case ch == ',' && !inQuote:
			items = append(items, current.String())
			current.Reset()
		default:
			current.WriteRune(ch)
		}
	}

	if current.Len() > 0 {
		items = append(items, current.String())
	}

	return items
}

func indexFirstSampleByName(samples []sample) map[string]sample {
	result := make(map[string]sample, len(samples))
	for _, s := range samples {
		if _, exists := result[s.name]; exists {
			continue
		}

		result[s.name] = s
	}

	return result
}

func formatSample(s canonicalSample) string {
	return fmt.Sprintf("%s%s %s", s.name, formatLabels(s.labels), formatFloat(s.value))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(labels))
	for _, key := range keys {
		parts = append(parts, key+`="`+escapeLabelValue(labels[key])+`"`)
	}

	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)

	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}

func labelsKey(labels map[string]string) string {
	return formatLabels(labels)
}
