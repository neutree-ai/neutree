package neutreemetrics

import (
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/prometheus/client_golang/prometheus"
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
	samples []normalizer.Sample
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

func newMetricsCollector(samples []normalizer.Sample) *metricsCollector {
	return &metricsCollector{samples: samples}
}

func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, descriptor := range metricDescriptors {
		ch <- descriptor.desc
	}
}

func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	for _, sample := range c.samples {
		descriptor := metricDescriptorByName[sample.Name]
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

		newMetricDescriptor("neutree_endpoint_replica_accelerator_allocation", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_memory_allocated_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_memory_used_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_endpoint_replica_accelerator_utilization_ratio", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_allocation", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_allocation_memory_allocated_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),
		newMetricDescriptor("neutree_node_accelerator_allocation_memory_used_bytes", endpointAcceleratorLabelNames, prometheus.GaugeValue, []string{"accelerator_uuid"}),

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
