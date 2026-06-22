package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

var splitDashboardFiles = []string{
	"neutree_overview_embed_dashboard.json",
	"neutree_endpoint_overview_embed_dashboard.json",
	"neutree_endpoint_latency_embed_dashboard.json",
	"neutree_endpoint_queue_embed_dashboard.json",
	"neutree_endpoint_cache_embed_dashboard.json",
	"neutree_cluster_overview_embed_dashboard.json",
	"neutree_cluster_gpu_embed_dashboard.json",
	"neutree_cluster_vgpu_embed_dashboard.json",
	"neutree_cluster_scheduling_embed_dashboard.json",
}

func TestSplitDashboardsAreValidAndDoNotUseRemovedMetrics(t *testing.T) {
	for _, file := range splitDashboardFiles {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)

			var dashboard map[string]any
			require.NoError(t, json.Unmarshal(raw, &dashboard))

			text := string(raw)
			assert.Contains(t, text, `"schemaVersion"`)
			assert.Contains(t, text, `"uid":`)
			assert.NotContains(t, text, "ray_serve")
			assert.NotContains(t, text, "ray_serve_deployment")
			assert.NotContains(t, text, "ray_serve_replica")
			assert.NotContains(t, text, "neutree_endpoint_status")
			assert.NotContains(t, text, "neutree_endpoint_token_capacity_per_second")
			assert.NotContains(t, text, "neutree_endpoint_gpu_request")
			assert.NotContains(t, text, "neutree_cluster_gpu_placeable")
			assert.NotContains(t, text, "neutree_cluster_gpu_fragmented")
			assert.NotContains(t, text, "neutree_risk_")
			assert.NotContains(t, text, "neutree_vgpu_")
		})
	}
}

func TestSplitDashboardPanelScope(t *testing.T) {
	assertDashboardPanels(t, "neutree_endpoint_latency_embed_dashboard.json",
		"TTFT",
		"ITL",
		"TPOT",
		"Prompt Length Distribution",
		"Generation Length Distribution",
	)
	assertDashboardPanels(t, "neutree_endpoint_overview_embed_dashboard.json",
		"Replicas",
		"Pending Replicas",
		"Failed Replicas",
		"Replicas with Devices",
	)
	assertDashboardPanels(t, "neutree_cluster_gpu_embed_dashboard.json",
		"Physical GPU Allocation",
	)
	assertDashboardPanels(t, "neutree_cluster_vgpu_embed_dashboard.json",
		"vGPU Allocation",
	)
	assertDashboardPanels(t, "neutree_cluster_scheduling_embed_dashboard.json",
		"Pending Reason Summary",
		"Failed Replicas",
	)
}

func TestSplitDashboardsArePackagedInHelmChart(t *testing.T) {
	for _, file := range splitDashboardFiles {
		t.Run(file, func(t *testing.T) {
			source, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)

			chart, err := os.ReadFile(filepath.Join("../../../deploy/chart/neutree/split-grafana-dashboards", file))
			require.NoError(t, err)
			assert.Equal(t, string(source), string(chart))
		})
	}
}

func assertDashboardPanels(t *testing.T, file string, titles ...string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)

	text := string(raw)
	for _, title := range titles {
		assert.Truef(t, strings.Contains(text, title), "%s should contain panel %q", file, title)
	}
}
