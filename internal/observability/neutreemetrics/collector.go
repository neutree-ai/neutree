package neutreemetrics

import (
	"sort"

	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
	"github.com/prometheus/client_golang/prometheus"
)

type metricsCollector struct {
	samples []normalizer.Sample
}

func newMetricsCollector(samples []normalizer.Sample) *metricsCollector {
	return &metricsCollector{samples: samples}
}

func (c *metricsCollector) Describe(ch chan<- *prometheus.Desc) {
	for _, desc := range c.descriptors() {
		ch <- desc
	}
}

func (c *metricsCollector) Collect(ch chan<- prometheus.Metric) {
	descs := c.descriptors()
	for _, sample := range c.samples {
		keys := sortedLabelKeys(sample.Labels)
		values := labelValues(sample.Labels, keys)
		desc := descs[descriptorKey(sample.Name, keys)]
		if desc == nil {
			continue
		}

		ch <- prometheus.MustNewConstMetric(
			desc,
			metricValueType(sample.Name),
			sample.Value,
			values...,
		)
	}
}

func (c *metricsCollector) descriptors() map[string]*prometheus.Desc {
	result := map[string]*prometheus.Desc{}
	for _, sample := range c.samples {
		keys := sortedLabelKeys(sample.Labels)
		key := descriptorKey(sample.Name, keys)
		if _, exists := result[key]; exists {
			continue
		}

		result[key] = prometheus.NewDesc(
			sample.Name,
			"Neutree node-agent metric "+sample.Name+".",
			keys,
			nil,
		)
	}

	return result
}

func descriptorKey(name string, labelKeys []string) string {
	return name + "\xff" + joinLabelKeys(labelKeys)
}

func sortedLabelKeys(labels map[string]string) []string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	return keys
}

func labelValues(labels map[string]string, keys []string) []string {
	values := make([]string, 0, len(keys))
	for _, key := range keys {
		values = append(values, labels[key])
	}

	return values
}

func joinLabelKeys(keys []string) string {
	result := ""
	for i, key := range keys {
		if i > 0 {
			result += "\xff"
		}
		result += key
	}

	return result
}

func metricValueType(name string) prometheus.ValueType {
	switch name {
	case "neutree_node_cpu_seconds_total",
		"neutree_accelerator_pcie_tx_bytes_total",
		"neutree_accelerator_pcie_rx_bytes_total",
		"neutree_endpoint_replica_cpu_usage_seconds_total":
		return prometheus.CounterValue
	default:
		return prometheus.GaugeValue
	}
}
