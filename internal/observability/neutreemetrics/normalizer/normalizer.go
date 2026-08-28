package normalizer

import (
	"cmp"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"

	prommodel "github.com/prometheus/common/model"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/promtext"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const (
	TargetNodeExporter        = "node-exporter"
	TargetAcceleratorExporter = "accelerator-exporter"

	unknownLabelValue = "unknown"
)

type NormalizeRequest struct {
	Labels       adapter.CanonicalLabels
	NodeExporter model.ScrapeResult
	// AcceleratorExporter is the adapter-selected exporter scrape result. The
	// normalizer reports its health but never interprets its vendor payload.
	AcceleratorExporter          *model.ScrapeResult
	EndpointReplicaRuntimeUsages []model.EndpointReplicaRuntimeUsage
	// AcceleratorSamples are adapter-owned accelerator samples. The shared
	// normalizer preserves them without applying a vendor fallback.
	AcceleratorSamples []Sample
}

// Normalizer renders only host-neutral node, runtime, and scrape-health data.
// Accelerator adapters are responsible for their own exporter semantics.
type Normalizer struct{}

type Sample struct {
	Name   string
	Labels map[string]string
	Value  float64
}

func (n *Normalizer) Samples(req NormalizeRequest) []Sample {
	var samples []Sample

	samples = append(samples, nodeReadySample(req.Labels))
	samples = append(samples, scrapeUpSample(req.Labels, TargetNodeExporter, req.NodeExporter.Up))

	if req.NodeExporter.Up {
		samples = append(samples, normalizeNodeSamples(req.Labels, req.NodeExporter.Body)...)
	}

	if req.AcceleratorExporter != nil {
		samples = append(samples, scrapeUpSample(req.Labels, TargetAcceleratorExporter, req.AcceleratorExporter.Up))
	}

	// The selected adapter has already converted accelerator evidence. Keep the
	// host neutral by only merging its declared samples.
	samples = append(samples, req.AcceleratorSamples...)
	samples = append(samples, normalizeEndpointReplicaRuntimeUsageSamples(
		req.Labels,
		req.EndpointReplicaRuntimeUsages,
	)...)

	sort.SliceStable(samples, func(i, j int) bool {
		if samples[i].Name == samples[j].Name {
			return labelsKey(samples[i].Labels) < labelsKey(samples[j].Labels)
		}

		return samples[i].Name < samples[j].Name
	})

	return samples
}

func normalizeNodeSamples(labels adapter.CanonicalLabels, raw string) []Sample {
	samples := promtext.ParseVector(raw)
	parsed := indexFirstSampleByName(samples)
	var result []Sample

	for _, sample := range samples {
		if promtext.MetricName(sample) != "node_cpu_seconds_total" {
			continue
		}

		metricLabels := baseLabels(labels, TargetNodeExporter)
		if cpu := promtext.LabelValue(sample, "cpu"); cpu != "" {
			metricLabels["cpu"] = cpu
		}

		if mode := promtext.LabelValue(sample, "mode"); mode != "" {
			metricLabels["mode"] = mode
		}

		result = append(result, Sample{
			Name:   "neutree_node_cpu_seconds_total",
			Labels: metricLabels,
			Value:  promtext.Value(sample),
		})
	}

	if total, ok := parsed["node_memory_MemTotal_bytes"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_memory_total_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(total),
		})
	}

	if available, ok := parsed["node_memory_MemAvailable_bytes"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_memory_available_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(available),
		})
	}

	total, hasTotal := parsed["node_memory_MemTotal_bytes"]
	available, hasAvailable := parsed["node_memory_MemAvailable_bytes"]

	if hasTotal && hasAvailable {
		result = append(result, Sample{
			Name:   "neutree_node_memory_used_bytes",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(total) - promtext.Value(available),
		})
	}

	if load1, ok := parsed["node_load1"]; ok {
		result = append(result, Sample{
			Name:   "neutree_node_load1",
			Labels: baseLabels(labels, TargetNodeExporter),
			Value:  promtext.Value(load1),
		})
	}

	return result
}

func normalizeEndpointReplicaRuntimeUsageSamples(
	labels adapter.CanonicalLabels,
	usages []model.EndpointReplicaRuntimeUsage,
) []Sample {
	result := make([]Sample, 0, len(usages)*5)

	for _, usage := range usages {
		metricLabels := endpointReplicaRuntimeUsageLabels(labels, usage)
		result = append(result, Sample{
			Name:   "neutree_endpoint_replica_cpu_usage_seconds_total",
			Labels: metricLabels,
			Value:  usage.CPUUsageSeconds,
		})

		if usage.MemoryUsageBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_usage_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryUsageBytes,
			})
		}

		if usage.MemoryWorkingSetBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_working_set_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryWorkingSetBytes,
			})
		}

		if usage.CPULimitCores != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_cpu_limit_cores",
				Labels: metricLabels,
				Value:  *usage.CPULimitCores,
			})
		}

		if usage.MemoryLimitBytes != nil {
			result = append(result, Sample{
				Name:   "neutree_endpoint_replica_memory_limit_bytes",
				Labels: metricLabels,
				Value:  *usage.MemoryLimitBytes,
			})
		}
	}

	return result
}

func endpointReplicaRuntimeUsageLabels(
	labels adapter.CanonicalLabels,
	usage model.EndpointReplicaRuntimeUsage,
) map[string]string {
	metricLabels := baseLabels(labels, model.SourceNodeAgent)
	metricLabels["endpoint"] = usage.Endpoint
	metricLabels["instance_id"] = usage.InstanceID
	metricLabels["replica"] = usage.ReplicaID
	metricLabels["node"] = cmp.Or(usage.NodeID, labels.Node)
	metricLabels["workload_role"] = labelValueOrUnknown(usage.WorkloadRole)
	metricLabels["container"] = usage.Container
	metricLabels["container_id"] = usage.ContainerID
	metricLabels["engine"] = usage.Engine
	metricLabels["engine_version"] = usage.EngineVersion

	return metricLabels
}

func nodeReadySample(labels adapter.CanonicalLabels) Sample {
	return Sample{
		Name:   "neutree_node_ready",
		Labels: baseLabels(labels, model.SourceNodeAgent),
		Value:  1,
	}
}

func scrapeUpSample(labels adapter.CanonicalLabels, target string, up bool) Sample {
	value := float64(0)
	if up {
		value = 1
	}

	metricLabels := baseLabels(labels, model.SourceNodeAgent)
	metricLabels["target"] = target

	return Sample{Name: "neutree_metrics_scrape_up", Labels: metricLabels, Value: value}
}

func labelValueOrUnknown(value string) string {
	if value == "" {
		return unknownLabelValue
	}

	return value
}

func baseLabels(labels adapter.CanonicalLabels, source string) map[string]string {
	result := map[string]string{"source": source}

	if labels.ClusterType != "" {
		result["cluster_type"] = labels.ClusterType
	}

	if labels.Node != "" {
		result["node"] = labels.Node
	}

	if labels.NodeIP != "" {
		result["node_ip"] = labels.NodeIP
	}

	if labels.NodeRole != "" {
		result["node_role"] = labels.NodeRole
	}

	return result
}

func indexFirstSampleByName(samples prommodel.Vector) map[string]*prommodel.Sample {
	result := make(map[string]*prommodel.Sample, len(samples))

	for _, sample := range samples {
		name := promtext.MetricName(sample)
		if _, exists := result[name]; !exists {
			result[name] = sample
		}
	}

	return result
}

func formatSample(sample Sample) string {
	return fmt.Sprintf("%s%s %s", sample.Name, formatLabels(sample.Labels), formatFloat(sample.Value))
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
