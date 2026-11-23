package neutree

import (
	"testing"

	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/yaml"
)

// A full trace buffer must drop traces, not back-pressure the shared Kong log
// source that also feeds usage recording.
func TestVectorTraceSinkDropsWhenBufferFull(t *testing.T) {
	resources := renderChart(t, nil)

	var vectorYAML string
	for _, resource := range resources.Items {
		if resource.GetKind() == "ConfigMap" && resource.GetName() == "neutree-vector" {
			data, _, err := unstructured.NestedString(resource.Object, "data", "vector.yml")
			require.NoError(t, err)
			vectorYAML = data
		}
	}
	require.NotEmpty(t, vectorYAML, "vector ConfigMap not rendered")

	var config struct {
		Sinks map[string]struct {
			Inputs []string               `json:"inputs"`
			Buffer map[string]interface{} `json:"buffer"`
		} `json:"sinks"`
	}
	require.NoError(t, yaml.Unmarshal([]byte(vectorYAML), &config))

	trace, ok := config.Sinks["ai_trace_victorialogs"]
	require.True(t, ok, "trace sink not rendered")
	require.Equal(t, "drop_newest", trace.Buffer["when_full"])

	usage, ok := config.Sinks["postgrest_rpc"]
	require.True(t, ok, "usage sink not rendered")
	require.NotContains(t, usage.Buffer, "when_full", "usage sink must keep the default blocking buffer")
}
