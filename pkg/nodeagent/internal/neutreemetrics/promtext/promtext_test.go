package promtext

import (
	"testing"

	"github.com/prometheus/common/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseUsesPrometheusTextParser(t *testing.T) {
	samples := ParseVector(`# HELP request_duration_seconds Request duration.
# TYPE request_duration_seconds histogram
request_duration_seconds_bucket{route="/v1/chat,stream",le="0.5"} 3
request_duration_seconds_bucket{route="/v1/chat,stream",le="+Inf"} 5
request_duration_seconds_sum{route="/v1/chat,stream"} 7
request_duration_seconds_count{route="/v1/chat,stream"} 5
# TYPE dcgm_metric gauge
dcgm_metric{modelName="Tesla T4",UUID="GPU-abc"} 87
`)

	require.NotEmpty(t, samples)
	assertSample(t, samples, "dcgm_metric", map[string]string{"UUID": "GPU-abc", "modelName": "Tesla T4"}, 87)
	assertSample(t, samples, "request_duration_seconds_bucket", map[string]string{"route": "/v1/chat,stream", "le": "0.5"}, 3)
}

func TestParseReturnsEmptyOnInvalidText(t *testing.T) {
	assert.Empty(t, ParseVector(`not a prometheus sample`))
}

func assertSample(t *testing.T, samples []*model.Sample, name string, labels map[string]string, value float64) {
	t.Helper()

	for _, sample := range samples {
		if MetricName(sample) != name || Value(sample) != value {
			continue
		}
		matches := true
		for key, expected := range labels {
			if LabelValue(sample, key) != expected {
				matches = false
				break
			}
		}
		if matches {
			return
		}
	}

	assert.Failf(t, "sample not found", "name=%s labels=%v value=%v", name, labels, value)
}
