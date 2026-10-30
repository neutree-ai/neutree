package logsink

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Shapes taken from Vector 0.47 prometheus_exporter output (host label trimmed).
const sampleMetrics = `# TYPE vector_component_sent_events_total counter
vector_component_sent_events_total{component_id="logsink_ok",component_kind="sink",component_type="http"} 5 1792233729442
vector_component_sent_events_total{component_id="ai_trace_victorialogs",component_kind="sink",component_type="http"} 9 1792233729442
# TYPE vector_component_errors_total counter
vector_component_errors_total{component_id="logsink_deny",component_kind="sink",component_type="http",error_type="request_failed",stage="sending"} 1 1792233729442
vector_component_errors_total{error_code="config_load",error_type="configuration_failed",stage="processing"} 2 1792233729442
vector_component_errors_total{error_code="reload",error_type="configuration_failed",stage="processing"} 3 1792233729442
# TYPE vector_component_discarded_events_total counter
vector_component_discarded_events_total{component_id="logsink_deny",component_kind="sink",component_type="http",intentional="false"} 5 1792233729442
# TYPE vector_buffer_discarded_events_total counter
vector_buffer_discarded_events_total{buffer_type="disk",component_id="logsink_down",component_kind="sink",component_type="http",intentional="true",stage="0"} 3 1792233729442
# TYPE vector_http_client_errors_total counter
vector_http_client_errors_total{component_id="logsink_down",component_kind="sink",component_type="http",error_kind="tcp connect error"} 7 1792233729442
# TYPE vector_http_client_responses_total counter
vector_http_client_responses_total{component_id="logsink_ok",component_kind="sink",component_type="http",status="200"} 1 1792233729442
vector_http_client_responses_total{component_id="logsink_deny",component_kind="sink",component_type="http",status="401"} 1 1792233729442
# TYPE vector_utilization gauge
vector_utilization{component_id="logsink_idle",component_kind="sink",component_type="http"} 0.002 1792233729442
# TYPE vector_reloaded_total counter
vector_reloaded_total 4 1792233729442
`

func TestParseMetrics(t *testing.T) {
	m, err := ParseMetrics(strings.NewReader(sampleMetrics))
	require.NoError(t, err)

	// Both a config that fails to load and a topology that fails to build are rejected reloads.
	assert.Equal(t, float64(5), m.RejectedReloads)
	assert.Equal(t, map[string]ComponentCounters{
		// Series alone do not show a sink is running.
		"logsink_ok":   {},
		"logsink_deny": {},
		"logsink_down": {},
		// Only utilization does; an idle one still reports it.
		"logsink_idle": {Running: true},
	}, m.Components)
}

func TestRejectedSince(t *testing.T) {
	m := &VectorMetrics{RejectedReloads: 3}
	assert.True(t, m.RejectedSince(2))
	assert.False(t, m.RejectedSince(3))
	// Expired and counted again from zero.
	assert.True(t, m.RejectedSince(5))

	m.RejectedReloads = 0
	assert.False(t, m.RejectedSince(5), "an expired series is not a rejection")
}
