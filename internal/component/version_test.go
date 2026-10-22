package component

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestReleaseVersionConstants(test *testing.T) {
	cases := []struct {
		name     string
		actual   string
		expected string
	}{
		{name: "HAMi", actual: HAMi, expected: "v2.9.0"},
		{name: "VictoriaMetrics", actual: VictoriaMetrics, expected: "v1.115.0"},
		{name: "VictoriaMetricsCluster", actual: VictoriaMetricsCluster, expected: "v1.115.0-cluster"},
		{name: "KubeStateMetrics", actual: KubeStateMetrics, expected: "v2.15.0"},
		{name: "NodeExporter", actual: NodeExporter, expected: "v1.8.2"},
		{name: "LegacyNeutreeNodeAgent", actual: LegacyNeutreeNodeAgent, expected: "v1.1.0-rc.1"},
		{name: "NeutreeNodeAgentV120", actual: NeutreeNodeAgentV120, expected: "v1.2.0-rc.1"},
		{name: "NeutreeNodeAgent", actual: NeutreeNodeAgent, expected: "v1.2.1-rc.1"},
		{name: "Grafana", actual: Grafana, expected: "11.5.3"},
		{name: "Vector", actual: Vector, expected: "0.47.0-debian"},
		{name: "Kong", actual: Kong, expected: "3.9"},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			assert.Equal(test, testCase.expected, testCase.actual)
		})
	}
}
