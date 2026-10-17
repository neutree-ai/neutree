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

	assert.Equal(t, float64(4), m.Reloads)
	assert.Equal(t, float64(2), m.ConfigLoadErrors)
	assert.Equal(t, map[string]ComponentCounters{
		"logsink_ok":   {Sent: 5},
		"logsink_deny": {Failed: 2, Dropped: 5},
		"logsink_down": {Failed: 7, Dropped: 3},
		// An idle sink still has series, so it counts as present.
		"logsink_idle": {},
	}, m.Components)
}

func TestComponentCountersSub(t *testing.T) {
	cur := ComponentCounters{Sent: 10, Failed: 2, Dropped: 1}
	assert.Equal(t, ComponentCounters{Sent: 4, Failed: 1}, cur.Sub(ComponentCounters{Sent: 6, Failed: 1, Dropped: 1}))
	// A counter that went backwards was reset: the current value is the change.
	assert.Equal(t, ComponentCounters{Sent: 10, Failed: 2, Dropped: 1}, cur.Sub(ComponentCounters{Sent: 50, Failed: 9, Dropped: 3}))
}
