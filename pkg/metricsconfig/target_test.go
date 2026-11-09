package metricsconfig

import (
	"testing"

	"github.com/VictoriaMetrics/VictoriaMetrics/lib/promrelabel"
	"github.com/stretchr/testify/require"
)

func TestRegistryOwnerReplaceRemoveAndDeterministicSnapshot(t *testing.T) {
	var registry Registry = NewRegistry()
	require.NoError(t, registry.Replace("producer/z", []Target{{JobName: "z-agent"}, {JobName: "a-exporter"}}))
	require.NoError(t, registry.Replace("producer/a", []Target{{JobName: "monitor"}}))
	snapshot, err := registry.Snapshot()
	require.NoError(t, err)
	require.Equal(t, []Target{{Owner: "producer/a", JobName: "monitor"}, {Owner: "producer/z", JobName: "a-exporter"}, {Owner: "producer/z", JobName: "z-agent"}}, snapshot.Targets)
	require.NoError(t, registry.Replace("producer/z", []Target{{JobName: "replacement"}}))
	snapshot, err = registry.Snapshot()
	require.NoError(t, err)
	require.Len(t, snapshot.Targets, 2)
	require.Equal(t, "replacement", snapshot.Targets[1].JobName)
	registry.Remove("producer/z")
	snapshot, err = registry.Snapshot()
	require.NoError(t, err)
	require.Equal(t, []Target{{Owner: "producer/a", JobName: "monitor"}}, snapshot.Targets)
	require.NoError(t, registry.Replace("producer/a", nil))
	snapshot, err = registry.Snapshot()
	require.NoError(t, err)
	require.Empty(t, snapshot.Targets)
}

func TestRegistryDeepCopiesProducerTargets(t *testing.T) {
	registry := NewRegistry()
	replacement := "$1:19101"
	input := Target{JobName: "node-agent", Namespace: "metrics-system", Selectors: PodSelectors(map[string]string{"neutree.ai/metrics-target": "node-agent"}),
		RelabelConfigs: []promrelabel.RelabelConfig{{Action: "replace", SourceLabels: []string{"__meta_kubernetes_pod_ip"},
			TargetLabel: "__address__", Regex: &promrelabel.MultiLineRegex{S: "(.+)"}, Replacement: &replacement}}}
	require.NoError(t, registry.Replace("operator/nvidia", []Target{input}))
	input.Selectors[0].Label = "changed"
	input.RelabelConfigs[0].SourceLabels[0] = "changed"
	input.RelabelConfigs[0].Regex.S = "changed"
	replacement = "changed"
	snapshot, err := registry.Snapshot()
	require.NoError(t, err)
	got := snapshot.Targets[0]
	require.Equal(t, "neutree.ai/metrics-target=node-agent", got.Selectors[0].Label)
	require.Equal(t, []string{"__meta_kubernetes_pod_ip"}, got.RelabelConfigs[0].SourceLabels)
	require.Equal(t, "(.+)", got.RelabelConfigs[0].Regex.S)
	require.Equal(t, "$1:19101", *got.RelabelConfigs[0].Replacement)
	got.Selectors[0].Label = "snapshot-change"
	got.RelabelConfigs[0].SourceLabels[0] = "snapshot-change"
	got.RelabelConfigs[0].Regex.S = "snapshot-change"
	*got.RelabelConfigs[0].Replacement = "snapshot-change"
	next, err := registry.Snapshot()
	require.NoError(t, err)
	require.Equal(t, "neutree.ai/metrics-target=node-agent", next.Targets[0].Selectors[0].Label)
	require.Equal(t, []string{"__meta_kubernetes_pod_ip"}, next.Targets[0].RelabelConfigs[0].SourceLabels)
	require.Equal(t, "(.+)", next.Targets[0].RelabelConfigs[0].Regex.S)
	require.Equal(t, "$1:19101", *next.Targets[0].RelabelConfigs[0].Replacement)
}
