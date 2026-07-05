package promtext

import (
	"sort"
	"strings"

	"github.com/prometheus/common/expfmt"
	prommodel "github.com/prometheus/common/model"
)

func ParseVector(raw string) prommodel.Vector {
	if raw != "" && !strings.HasSuffix(raw, "\n") {
		raw += "\n"
	}

	var parser expfmt.TextParser
	families, err := parser.TextToMetricFamilies(strings.NewReader(raw))
	if err != nil {
		return nil
	}

	familyNames := make([]string, 0, len(families))
	for name := range families {
		familyNames = append(familyNames, name)
	}
	sort.Strings(familyNames)

	result := prommodel.Vector{}
	for _, familyName := range familyNames {
		vector, err := expfmt.ExtractSamples(&expfmt.DecodeOptions{}, families[familyName])
		if err != nil {
			continue
		}

		result = append(result, vector...)
	}

	sort.SliceStable(result, func(i, j int) bool {
		if MetricName(result[i]) == MetricName(result[j]) {
			return labelsKey(result[i].Metric) < labelsKey(result[j].Metric)
		}

		return MetricName(result[i]) < MetricName(result[j])
	})

	return result
}

func MetricName(sample *prommodel.Sample) string {
	if sample == nil {
		return ""
	}

	return string(sample.Metric[prommodel.MetricNameLabel])
}

func LabelValue(sample *prommodel.Sample, names ...string) string {
	if sample == nil {
		return ""
	}

	for _, name := range names {
		value := string(sample.Metric[prommodel.LabelName(name)])
		if value != "" {
			return value
		}
	}

	return ""
}

func Labels(sample *prommodel.Sample) map[string]string {
	labels := map[string]string{}
	if sample == nil {
		return labels
	}

	for name, value := range sample.Metric {
		if name == prommodel.MetricNameLabel {
			continue
		}

		labels[string(name)] = string(value)
	}

	return labels
}

func Value(sample *prommodel.Sample) float64 {
	if sample == nil {
		return 0
	}

	return float64(sample.Value)
}

func labelsKey(labels prommodel.Metric) string {
	keys := make([]string, 0, len(labels))
	for key := range labels {
		if key == prommodel.MetricNameLabel {
			continue
		}
		keys = append(keys, string(key))
	}
	sort.Strings(keys)

	var builder strings.Builder
	for _, key := range keys {
		builder.WriteString(key)
		builder.WriteByte('=')
		builder.WriteString(string(labels[prommodel.LabelName(key)]))
		builder.WriteByte(',')
	}

	return builder.String()
}
