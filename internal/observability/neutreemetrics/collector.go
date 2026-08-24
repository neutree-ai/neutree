package neutreemetrics

import (
	"fmt"
	"math"
	"sort"
	"strings"

	"github.com/prometheus/client_golang/prometheus"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

const missingLabelValue = "unknown"

var (
	baseNodeLabelNames = []string{
		"cluster_type",
		"node",
		"node_ip",
		"node_role",
		"source",
	}
	physicalAcceleratorLabelNames = []string{
		"cluster_type",
		"node",
		"accelerator_type",
		"accelerator_uuid",
		"accelerator_index",
		"product",
	}
	nodeAcceleratorProductLabelNames = []string{
		"cluster_type",
		"node",
		"accelerator_type",
		"product",
	}
	endpointAcceleratorLabelNames = []string{
		"cluster_type",
		"endpoint",
		"instance_id",
		"replica",
		"node",
		"accelerator_type",
		"accelerator_uuid",
		"accelerator_index",
		"vdevice_index",
		"product",
	}
	endpointAcceleratorAllocationLabelNames = appendLabels(endpointAcceleratorLabelNames,
		"vram_usage",
		"physical_vram_usage",
	)
	endpointRuntimeLabelNames = []string{
		"cluster_type",
		"node",
		"node_ip",
		"node_role",
		"source",
		"endpoint",
		"instance_id",
		"replica",
		"workload_role",
		"container",
		"container_id",
		"engine",
		"engine_version",
	}
	hardwareInfoLabelNames = appendLabels(physicalAcceleratorLabelNames,
		"memory_total_bytes",
		"pcie_bus_id",
		"pcie_generation",
		"pcie_width",
		"numa_node",
	)
	nvidiaInfoLabelNames = appendLabels(physicalAcceleratorLabelNames,
		"architecture",
		"cuda_capability",
		"driver_version",
		"cuda_driver_version",
		"nvlink",
		"nvswitch",
	)
	metricDescriptors      = newMetricDescriptors()
	metricDescriptorByName = indexMetricDescriptors(metricDescriptors)
)

type metricsCollector struct {
	samples          []normalizer.Sample
	descriptors      []*metricDescriptor
	descriptorByName map[string]*metricDescriptor
}

// metricDescriptor keeps the native prometheus.Desc with its Neutree sample name
// and label validation metadata so the normalizer can stay sample-oriented.
type metricDescriptor struct {
	name         string
	labelNames   []string
	valueType    prometheus.ValueType
	requiredKeys []string
	desc         *prometheus.Desc
}

func newMetricsCollector(
	samples []normalizer.Sample,
	adapterDescriptorGroups ...[]adapter.MetricDescriptor,
) *metricsCollector {
	descriptors := append([]*metricDescriptor{}, metricDescriptors...)

	for _, group := range adapterDescriptorGroups {
		for _, descriptor := range group {
			descriptors = append(descriptors, newMetricDescriptor(
				descriptor.Name,
				append([]string{}, descriptor.LabelNames...),
				prometheus.GaugeValue,
				append([]string{}, descriptor.RequiredLabelNames...),
			))
		}
	}

	return &metricsCollector{
		samples:          samples,
		descriptors:      descriptors,
		descriptorByName: indexMetricDescriptors(descriptors),
	}
}

func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, descriptor := range c.descriptors {
		ch <- descriptor.desc
	}
}

func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	for _, sample := range c.samples {
		descriptor := c.descriptorByName[sample.Name]
		if descriptor == nil || !hasRequiredLabels(sample.Labels, descriptor.requiredKeys) {
			continue
		}

		ch <- prometheus.MustNewConstMetric(
			descriptor.desc,
			descriptor.valueType,
			sample.Value,
			fixedLabelValues(sample.Labels, descriptor.labelNames)...,
		)
	}
}

func validateAdapterMetricDescriptors(descriptors []adapter.MetricDescriptor) error {
	seenNames := make(map[string]struct{}, len(metricDescriptorByName)+len(descriptors))

	for name := range metricDescriptorByName {
		seenNames[name] = struct{}{}
	}

	for _, descriptor := range descriptors {
		name := strings.TrimSpace(descriptor.Name)

		if name == "" {
			return fmt.Errorf("accelerator metric descriptor name is required")
		}

		if _, exists := seenNames[name]; exists {
			return fmt.Errorf("accelerator metric descriptor %q conflicts with an existing descriptor", name)
		}

		seenNames[name] = struct{}{}

		labelNames := make(map[string]struct{}, len(descriptor.LabelNames))

		for _, labelName := range descriptor.LabelNames {
			labelName = strings.TrimSpace(labelName)

			if labelName == "" {
				return fmt.Errorf("accelerator metric descriptor %q has an empty label name", name)
			}

			if _, exists := labelNames[labelName]; exists {
				return fmt.Errorf("accelerator metric descriptor %q has duplicate label %q", name, labelName)
			}

			labelNames[labelName] = struct{}{}
		}

		requiredLabelNames := make(map[string]struct{}, len(descriptor.RequiredLabelNames))

		for _, labelName := range descriptor.RequiredLabelNames {
			labelName = strings.TrimSpace(labelName)

			if labelName == "" {
				return fmt.Errorf("accelerator metric descriptor %q has an empty required label name", name)
			}

			if _, exists := labelNames[labelName]; !exists {
				return fmt.Errorf("accelerator metric descriptor %q requires unknown label %q", name, labelName)
			}

			if _, exists := requiredLabelNames[labelName]; exists {
				return fmt.Errorf("accelerator metric descriptor %q has duplicate required label %q", name, labelName)
			}

			requiredLabelNames[labelName] = struct{}{}
		}
	}

	return nil
}

// ValidateAdapterMetricDescriptors validates extension descriptors against the
// built-in NodeAgent descriptor set. The public host calls this before parsing
// runtime options so an invalid adapter image fails fast.
func ValidateAdapterMetricDescriptors(descriptors []adapter.MetricDescriptor) error {
	return validateAdapterMetricDescriptors(descriptors)
}

// validateAdapterSamples validates adapter output before it is converted to
// internal normalizer samples. Adapters may emit built-in descriptors or their
// declared extension descriptors, but cannot introduce an undeclared series.
func validateAdapterSamples(samples []adapter.Sample, extensionDescriptors []adapter.MetricDescriptor) error {
	descriptors := make(map[string]*metricDescriptor, len(metricDescriptorByName)+len(extensionDescriptors))
	for name, descriptor := range metricDescriptorByName {
		descriptors[name] = descriptor
	}
	for _, descriptor := range extensionDescriptors {
		descriptors[descriptor.Name] = newMetricDescriptor(
			descriptor.Name,
			append([]string(nil), descriptor.LabelNames...),
			prometheus.GaugeValue,
			append([]string(nil), descriptor.RequiredLabelNames...),
		)
	}

	seen := make(map[string]struct{}, len(samples))
	for _, sample := range samples {
		name := strings.TrimSpace(sample.Name)
		if name == "" {
			return fmt.Errorf("accelerator sample name is required")
		}
		descriptor := descriptors[name]
		if descriptor == nil {
			return fmt.Errorf("accelerator sample %q has no declared descriptor", name)
		}
		if math.IsNaN(sample.Value) || math.IsInf(sample.Value, 0) {
			return fmt.Errorf("accelerator sample %q has a non-finite value", name)
		}

		allowedLabels := make(map[string]struct{}, len(descriptor.labelNames))
		for _, labelName := range descriptor.labelNames {
			allowedLabels[labelName] = struct{}{}
		}
		for labelName := range sample.Labels {
			if _, ok := allowedLabels[labelName]; !ok {
				return fmt.Errorf("accelerator sample %q has undeclared label %q", name, labelName)
			}
		}
		if !hasRequiredLabels(sample.Labels, descriptor.requiredKeys) {
			return fmt.Errorf("accelerator sample %q is missing a required label", name)
		}

		key := adapterSampleKey(name, sample.Labels)
		if _, exists := seen[key]; exists {
			return fmt.Errorf("duplicate accelerator sample %q", key)
		}
		seen[key] = struct{}{}
	}

	return nil
}

func adapterSampleKey(name string, labels map[string]string) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var builder strings.Builder
	builder.WriteString(name)
	for _, key := range keys {
		builder.WriteByte('\x00')
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(labels[key])
	}

	return builder.String()
}

func fixedLabelValues(labels map[string]string, labelNames []string) []string {
	values := make([]string, 0, len(labelNames))
	for _, key := range labelNames {
		values = append(values, labelValue(labels, key))
	}

	return values
}

func labelValue(labels map[string]string, key string) string {
	if value := labels[key]; value != "" {
		return value
	}

	if key == "vdevice_index" {
		return "0"
	}

	return missingLabelValue
}

func hasRequiredLabels(labels map[string]string, keys []string) bool {
	for _, key := range keys {
		if labels[key] == "" {
			return false
		}
	}

	return true
}

func newMetricDescriptors() []*metricDescriptor {
	descriptors := []*metricDescriptor{
		newMetricDescriptor("neutree_node_ready", baseNodeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_metrics_scrape_up", appendLabels(baseNodeLabelNames, "target"), prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_cpu_seconds_total", appendLabels(baseNodeLabelNames, "cpu", "mode"), prometheus.CounterValue, nil),
		newMetricDescriptor("neutree_node_memory_total_bytes", baseNodeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_memory_available_bytes", baseNodeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_memory_used_bytes", baseNodeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_load1", baseNodeLabelNames, prometheus.GaugeValue, nil),

		newMetricDescriptor("neutree_accelerator_utilization_ratio", physicalAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_accelerator_memory_used_bytes", physicalAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_accelerator_memory_total_bytes", physicalAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_accelerator_temperature_celsius", physicalAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_accelerator_pcie_tx_bytes_total", physicalAcceleratorLabelNames, prometheus.CounterValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_accelerator_pcie_rx_bytes_total", physicalAcceleratorLabelNames, prometheus.CounterValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_info", physicalAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_total", nodeAcceleratorProductLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_accelerator_allocated", nodeAcceleratorProductLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_accelerator_free", nodeAcceleratorProductLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_node_accelerator_hardware_info", hardwareInfoLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_nvidia_info", nvidiaInfoLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),

		newMetricDescriptor("neutree_endpoint_replica_accelerator_allocation", endpointAcceleratorAllocationLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_memory_allocated_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_memory_used_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_utilization_ratio", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),

		newMetricDescriptor("neutree_endpoint_replica_cpu_usage_seconds_total", endpointRuntimeLabelNames, prometheus.CounterValue, nil),
		newMetricDescriptor("neutree_endpoint_replica_memory_usage_bytes", endpointRuntimeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_endpoint_replica_memory_working_set_bytes", endpointRuntimeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_endpoint_replica_cpu_limit_cores", endpointRuntimeLabelNames, prometheus.GaugeValue, nil),
		newMetricDescriptor("neutree_endpoint_replica_memory_limit_bytes", endpointRuntimeLabelNames, prometheus.GaugeValue, nil),
	}

	return descriptors
}

func newMetricDescriptor(
	name string,
	labelNames []string,
	valueType prometheus.ValueType,
	requiredKeys []string,
) *metricDescriptor {
	return &metricDescriptor{
		name:         name,
		labelNames:   labelNames,
		valueType:    valueType,
		requiredKeys: requiredKeys,
		desc: prometheus.NewDesc(
			name,
			"Neutree node-agent metric "+name+".",
			labelNames,
			nil,
		),
	}
}

func indexMetricDescriptors(descriptors []*metricDescriptor) map[string]*metricDescriptor {
	result := make(map[string]*metricDescriptor, len(descriptors))
	for _, descriptor := range descriptors {
		result[descriptor.name] = descriptor
	}

	return result
}

func appendLabels(base []string, labels ...string) []string {
	result := make([]string, 0, len(base)+len(labels))
	result = append(result, base...)
	result = append(result, labels...)

	return result
}
