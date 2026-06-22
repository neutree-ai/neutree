package neutreemetrics

import (
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	TargetNodeExporter        = "node-exporter"
	TargetAcceleratorExporter = "accelerator-exporter"
)

type CanonicalLabels struct {
	Workspace         string
	NeutreeCluster    string
	StaticNodeCluster string
	ClusterType       string
	Node              string
	NodeIP            string
	NodeRole          string
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
	EndpointAllocations []EndpointAllocation
}

type EndpointAllocation struct {
	Workspace  string
	Cluster    string
	Endpoint   string
	InstanceID string
	ReplicaID  string
	NodeID     string
	Devices    []v1.DeviceAllocation
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

	samples = append(samples, nodeReadySample(req.Labels))
	samples = append(samples, scrapeUpSample(req.Labels, TargetNodeExporter, req.NodeExporter.Up))
	if req.NodeExporter.Up {
		samples = append(samples, normalizeNodeSamples(req.Labels, req.NodeExporter.Body)...)
	}

	if req.AcceleratorExporter != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetAcceleratorExporter, req.AcceleratorExporter.Up))
		if req.AcceleratorExporter.Up {
			samples = append(samples, normalizeAcceleratorSamples(req.Labels, req.AcceleratorExporter.Body)...)
			samples = append(samples, normalizeNodeGPUSamples(
				req.Labels,
				req.AcceleratorExporter.Body,
				req.EndpointAllocations,
			)...)
		}
	}

	samples = append(samples, normalizeEndpointAllocationSamples(req.Labels, req.EndpointAllocations)...)

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

func normalizeAcceleratorSamples(labels CanonicalLabels, raw string) []canonicalSample {
	parsed := parsePrometheusText(raw)
	result := make([]canonicalSample, 0)

	for _, s := range parsed {
		metricLabels, ok := acceleratorMetricLabels(labels, s)
		if !ok {
			continue
		}

		switch s.name {
		case "DCGM_FI_DEV_GPU_UTIL":
			value := s.value
			if value > 1 {
				value /= 100
			}

			result = append(result, canonicalSample{
				name:   "neutree_gpu_utilization_ratio",
				labels: metricLabels,
				value:  value,
			})
		case "DCGM_FI_DEV_FB_USED":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_used_bytes",
				labels: metricLabels,
				value:  s.value * 1024 * 1024,
			})
		case "DCGM_FI_DEV_FB_TOTAL":
			result = append(result, canonicalSample{
				name:   "neutree_gpu_memory_total_bytes",
				labels: metricLabels,
				value:  s.value * 1024 * 1024,
			})
		}
	}

	return result
}

func normalizeNodeGPUSamples(
	labels CanonicalLabels,
	raw string,
	allocations []EndpointAllocation,
) []canonicalSample {
	devices := acceleratorDevicesFromMetrics(raw)
	if len(devices) == 0 {
		return nil
	}

	allocatedByUUID := allocatedDeviceUUIDs(allocations)
	totalByProduct := map[string]float64{}
	allocatedByProduct := map[string]float64{}
	result := make([]canonicalSample, 0, len(devices)*2+len(totalByProduct)*3)

	for _, device := range devices {
		if device.UUID == "" {
			continue
		}

		product := firstNonEmpty(device.ProductModel, device.ProductName, v1.AcceleratorTypeNVIDIAGPU.String())
		totalByProduct[product]++

		if _, ok := allocatedByUUID[device.UUID]; ok {
			allocatedByProduct[product]++
		}

		metricLabels := nodeGPULabels(labels, product)
		metricLabels["gpu_uuid"] = device.UUID
		if device.ID != "" {
			metricLabels["gpu_index"] = device.ID
		}

		result = append(result, canonicalSample{
			name:   "neutree_node_gpu_info",
			labels: metricLabels,
			value:  1,
		})
	}

	products := make([]string, 0, len(totalByProduct))
	for product := range totalByProduct {
		products = append(products, product)
	}
	sort.Strings(products)

	for _, product := range products {
		total := totalByProduct[product]
		allocated := allocatedByProduct[product]
		free := total - allocated
		metricLabels := nodeGPULabels(labels, product)

		result = append(result,
			canonicalSample{
				name:   "neutree_node_gpu_total",
				labels: cloneLabels(metricLabels),
				value:  total,
			},
			canonicalSample{
				name:   "neutree_node_gpu_allocated",
				labels: cloneLabels(metricLabels),
				value:  allocated,
			},
			canonicalSample{
				name:   "neutree_node_gpu_free",
				labels: cloneLabels(metricLabels),
				value:  free,
			},
		)
	}

	return result
}

func normalizeEndpointAllocationSamples(
	labels CanonicalLabels,
	allocations []EndpointAllocation,
) []canonicalSample {
	result := make([]canonicalSample, 0)

	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			metricLabels := baseLabels(labels, SourceNodeAgent)
			metricLabels["workspace"] = firstNonEmpty(allocation.Workspace, labels.Workspace)
			metricLabels["neutree_cluster"] = firstNonEmpty(allocation.Cluster, labels.NeutreeCluster, labels.StaticNodeCluster)
			metricLabels["endpoint"] = allocation.Endpoint
			metricLabels["instance_id"] = allocation.InstanceID
			metricLabels["replica_id"] = allocation.ReplicaID
			metricLabels["node"] = firstNonEmpty(allocation.NodeID, device.NodeID, labels.Node)
			metricLabels["gpu_uuid"] = device.UUID
			metricLabels["product"] = device.Product

			result = append(result, canonicalSample{
				name:   "neutree_endpoint_replica_gpu_allocation",
				labels: metricLabels,
				value:  1,
			})

			nodeGPUMetricLabels := cloneLabels(metricLabels)
			nodeGPUMetricLabels["replica"] = allocation.ReplicaID

			result = append(result, canonicalSample{
				name:   "neutree_node_gpu_allocation",
				labels: nodeGPUMetricLabels,
				value:  1,
			})
		}
	}

	return result
}

func nodeReadySample(labels CanonicalLabels) canonicalSample {
	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)

	return canonicalSample{
		name:   "neutree_node_ready",
		labels: metricLabels,
		value:  1,
	}
}

func scrapeUpSample(labels CanonicalLabels, target string, up bool) canonicalSample {
	value := float64(0)
	if up {
		value = 1
	}

	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["target"] = target

	return canonicalSample{
		name:   "neutree_metrics_scrape_up",
		labels: metricLabels,
		value:  value,
	}
}

func nodeGPULabels(labels CanonicalLabels, product string) map[string]string {
	metricLabels := baseLabels(labels, SourceNodeAgent)
	metricLabels["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)
	metricLabels["accelerator_type"] = v1.AcceleratorTypeNVIDIAGPU.String()
	metricLabels["product"] = product

	return metricLabels
}

func allocatedDeviceUUIDs(allocations []EndpointAllocation) map[string]struct{} {
	result := map[string]struct{}{}
	for _, allocation := range allocations {
		for _, device := range allocation.Devices {
			if device.UUID == "" {
				continue
			}

			result[device.UUID] = struct{}{}
		}
	}

	return result
}

func cloneLabels(labels map[string]string) map[string]string {
	result := make(map[string]string, len(labels))
	for key, value := range labels {
		result[key] = value
	}

	return result
}

func baseLabels(labels CanonicalLabels, source string) map[string]string {
	clusterType := labels.ClusterType
	if clusterType == "" {
		clusterType = "ray"
	}

	return map[string]string{
		"workspace":           labels.Workspace,
		"static_node_cluster": labels.StaticNodeCluster,
		"cluster_type":        clusterType,
		"node":                labels.Node,
		"node_ip":             labels.NodeIP,
		"node_role":           labels.NodeRole,
		"source":              source,
	}
}

func acceleratorMetricLabels(labels CanonicalLabels, s sample) (map[string]string, bool) {
	uuid := firstNonEmpty(s.labels["UUID"], s.labels["uuid"])
	if uuid == "" {
		return nil, false
	}

	result := baseLabels(labels, TargetAcceleratorExporter)
	result["neutree_cluster"] = firstNonEmpty(labels.NeutreeCluster, labels.StaticNodeCluster)
	result["gpu_uuid"] = uuid

	if gpuIndex := firstNonEmpty(s.labels["gpu"], s.labels["GPU_I_ID"]); gpuIndex != "" {
		result["gpu_index"] = gpuIndex
	}

	if model := firstNonEmpty(s.labels["modelName"], s.labels["model"]); model != "" {
		result["model"] = model
	}

	return result, true
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if value != "" {
			return value
		}
	}

	return ""
}

func parsePrometheusText(raw string) []sample {
	var result []sample

	for _, line := range strings.Split(raw, "\n") {
		line = strings.TrimSpace(line)

		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		metricPart, valuePart, ok := splitPrometheusSampleLine(line)
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

func splitPrometheusSampleLine(line string) (string, string, bool) {
	escaped := false
	inQuote := false

	for index, ch := range line {
		switch {
		case escaped:
			escaped = false
		case ch == '\\':
			escaped = true
		case ch == '"':
			inQuote = !inQuote
		case (ch == ' ' || ch == '\t') && !inQuote:
			metricPart := strings.TrimSpace(line[:index])
			valuePart := strings.TrimSpace(line[index:])

			return metricPart, valuePart, metricPart != "" && valuePart != ""
		}
	}

	return "", "", false
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
