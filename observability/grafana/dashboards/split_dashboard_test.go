package dashboards

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
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
	"neutree_endpoint_token_latency_embed_dashboard.json",
	"neutree_cluster_overview_embed_dashboard.json",
}

type panelConfig struct {
	Title           string                 `json:"title"`
	Type            string                 `json:"type"`
	Description     string                 `json:"description"`
	GridPos         gridPosConfig          `json:"gridPos"`
	FieldConfig     map[string]any         `json:"fieldConfig"`
	Options         map[string]any         `json:"options"`
	Transformations []transformationConfig `json:"transformations"`
	Targets         []struct {
		Expr         string `json:"expr"`
		RefID        string `json:"refId"`
		Format       string `json:"format"`
		Instant      bool   `json:"instant"`
		LegendFormat string `json:"legendFormat"`
		Range        bool   `json:"range"`
	} `json:"targets"`
}

type gridPosConfig struct {
	H int `json:"h"`
	W int `json:"w"`
	X int `json:"x"`
	Y int `json:"y"`
}

type transformationConfig struct {
	ID      string         `json:"id"`
	Options map[string]any `json:"options"`
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
			assertSplitDashboardDatasource(t, dashboard)
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
			assert.NotContains(t, text, "Current risks")
			assert.NotContains(t, text, "Capacity vs demand")
			assert.NotContains(t, text, "Top hot devices")
			assert.NotContains(t, text, "Placeable GPUs")
			assert.NotContains(t, text, "Fragmented")
			assert.NotContains(t, text, `"title": "Failed"`)
			assert.NotContains(t, text, `"title": "Pending"`)
			assert.NotContains(t, text, "neutree_endpoint_pending_replicas")
			assert.NotContains(t, text, "Slowest replicas")
			assert.NotContains(t, text, "Prompt length heatmap")
		})
	}
}

func TestSplitDashboardsDoNotFallbackToRawExporterMetrics(t *testing.T) {
	rawFallbackMetrics := []string{
		"DCGM_FI_DEV_GPU_UTIL",
		"DCGM_FI_DEV_FB_USED",
		"DCGM_FI_DEV_FB_TOTAL",
		"node_memory_MemAvailable_bytes",
		"node_memory_MemTotal_bytes",
	}

	for _, file := range splitDashboardFiles {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)

			text := string(raw)
			for _, metric := range rawFallbackMetrics {
				assert.NotContains(t, text, metric)
			}
			assert.NotContains(t, text, "rate(node_cpu_seconds_total")
		})
	}
}

func TestTablePanelsDoNotExposeGenericValueColumns(t *testing.T) {
	for _, file := range splitDashboardFiles {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)

			var dashboard struct {
				Panels []panelConfig `json:"panels"`
			}
			require.NoError(t, json.Unmarshal(raw, &dashboard))

			for _, panel := range dashboard.Panels {
				if !isTablePanel(panel) {
					continue
				}

				organize := transformationOptions(t, panel, "organize")
				indexByName, ok := organize["indexByName"].(map[string]any)
				if !ok {
					continue
				}

				for field := range indexByName {
					if !strings.HasPrefix(field, "Value") {
						continue
					}

					assert.Truef(t,
						organizeHidesField(organize, field) || organizeRenamesValueField(organize, field),
						"%s panel %q exposes generic table field %q", file, panel.Title, field,
					)
				}
			}
		})
	}
}

func TestSplitDashboardPanelScope(t *testing.T) {
	assertDashboardPanelTitles(t, "neutree_overview_embed_dashboard.json",
		"Nodes",
		"GPU",
		"GPU avg",
		"GPU memory",
		"Current QPS",
		"Avg latency",
		"CPU avg",
		"Memory avg",
		"GPU inventory by model",
		"Cluster utilization",
	)
	assertDashboardPanelTitles(t, "neutree_endpoint_overview_embed_dashboard.json",
		"Consumed GPUs",
		"VRAM used",
		"CPU cores",
		"Memory working set",
		"GPU utilization trend",
		"VRAM utilization trend",
		"CPU usage trend",
		"Memory usage trend",
		"GPU allocation details",
	)
	assertDashboardPanelTitles(t, "neutree_endpoint_latency_embed_dashboard.json",
		"E2E P95",
		"TTFT P95",
		"TPOT P95",
		"ITL P95",
		"E2E latency trend",
		"TTFT latency trend",
		"TPOT latency trend",
		"ITL latency trend",
	)
	assertDashboardPanelTitles(t, "neutree_endpoint_queue_embed_dashboard.json",
		"Running",
		"Waiting",
		"Preemptions / Retractions",
		"Queue time P95",
		"Queue status trend",
		"Queue time trend",
	)
	assertDashboardPanelTitles(t, "neutree_endpoint_cache_embed_dashboard.json",
		"KV token capacity",
		"KV usage",
		"Cache hit",
		"KV usage trend",
		"Cache hit trend",
	)
	assertDashboardPanelTitles(t, "neutree_endpoint_token_latency_embed_dashboard.json",
		"Prompt tokens/s",
		"Generation tokens/s",
		"Total tokens/s",
		"Token throughput trend",
		"Request Prompt Length",
		"Request Generation Length",
	)
	assertDashboardPanelTitles(t, "neutree_cluster_overview_embed_dashboard.json",
		"Nodes",
		"Total GPUs",
		"Free GPUs",
		"GPU avg",
		"VRAM avg",
		"Utilization trend",
		"GPU allocation details",
	)
}

func TestSplitDashboardPanelMetricScope(t *testing.T) {
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "Nodes",
		"neutree_node_ready")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "GPU",
		"neutree_node_gpu_total")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "GPU inventory by model",
		"neutree_node_gpu_total", "neutree_node_gpu_free")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "Current QPS",
		"vllm:request_success_total", "sglang_num_requests_total", "sglang_e2e_request_latency_seconds_count")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "Avg latency",
		"vllm:e2e_request_latency_seconds_sum", "vllm:e2e_request_latency_seconds_count",
		"sglang_e2e_request_latency_seconds_sum", "sglang_e2e_request_latency_seconds_count")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "CPU avg",
		"neutree_node_cpu_seconds_total")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "Memory avg",
		"neutree_node_memory_used_bytes", "neutree_node_memory_total_bytes")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "GPU avg",
		"neutree_gpu_utilization_ratio")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "GPU memory",
		"neutree_gpu_memory_used_bytes", "neutree_gpu_memory_total_bytes")
	assertPanelMetricScope(t, "neutree_overview_embed_dashboard.json", "Cluster utilization",
		"neutree_node_cpu_seconds_total", "neutree_node_memory_used_bytes", "neutree_node_memory_total_bytes",
		"neutree_gpu_utilization_ratio", "neutree_gpu_memory_used_bytes", "neutree_gpu_memory_total_bytes")

	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "Consumed GPUs",
		"neutree_node_gpu_allocation")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "VRAM used",
		"neutree_gpu_memory_used_bytes", "neutree_node_gpu_allocation")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "CPU cores",
		"neutree_endpoint_replica_cpu_usage_seconds_total")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "Memory working set",
		"neutree_endpoint_replica_memory_working_set_bytes")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "GPU allocation details",
		"neutree_node_gpu_allocation_info", "neutree_node_gpu_hardware_info",
		"neutree_gpu_temperature_celsius", "vllm:num_requests_running", "vllm:request_success_total",
		"sglang_num_running_reqs", "sglang_num_requests_total")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "GPU utilization trend",
		"neutree_gpu_utilization_ratio", "neutree_node_gpu_allocation_info")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "VRAM utilization trend",
		"neutree_gpu_memory_used_bytes", "neutree_gpu_memory_total_bytes", "neutree_node_gpu_allocation_info")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "CPU usage trend",
		"neutree_endpoint_replica_cpu_usage_seconds_total")
	assertPanelMetricScope(t, "neutree_endpoint_overview_embed_dashboard.json", "Memory usage trend",
		"neutree_endpoint_replica_memory_working_set_bytes")

	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "E2E P95",
		"vllm:e2e_request_latency_seconds_bucket", "sglang_e2e_request_latency_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "TTFT P95",
		"vllm:time_to_first_token_seconds_bucket", "sglang_time_to_first_token_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "TPOT P95",
		"vllm:request_time_per_output_token_seconds_bucket", "vllm:time_per_output_token_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "ITL P95",
		"vllm:inter_token_latency_seconds_bucket", "sglang_inter_token_latency_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "E2E latency trend",
		"vllm:e2e_request_latency_seconds_bucket", "sglang_e2e_request_latency_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "TTFT latency trend",
		"vllm:time_to_first_token_seconds_bucket", "sglang_time_to_first_token_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "TPOT latency trend",
		"vllm:request_time_per_output_token_seconds_bucket", "vllm:time_per_output_token_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_latency_embed_dashboard.json", "ITL latency trend",
		"vllm:inter_token_latency_seconds_bucket", "sglang_inter_token_latency_seconds_bucket")

	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Running",
		"vllm:num_requests_running", "sglang_num_running_reqs")
	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Waiting",
		"vllm:num_requests_waiting", "sglang_num_queue_reqs")
	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Preemptions / Retractions",
		"vllm:num_preemptions_total", "sglang_num_retracted_requests_total")
	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Queue time P95",
		"vllm:request_queue_time_seconds_bucket", "sglang_queue_time_seconds_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Queue status trend",
		"vllm:num_requests_running", "sglang_num_running_reqs",
		"vllm:num_requests_waiting", "sglang_num_queue_reqs",
		"vllm:num_preemptions_total", "sglang_num_retracted_requests_total")
	assertPanelMetricScope(t, "neutree_endpoint_queue_embed_dashboard.json", "Queue time trend",
		"vllm:request_queue_time_seconds_bucket", "sglang_queue_time_seconds_bucket")

	assertPanelMetricScope(t, "neutree_endpoint_cache_embed_dashboard.json", "KV token capacity",
		"vllm:cache_config_info", "sglang_max_total_num_tokens")
	assertPanelMetricScope(t, "neutree_endpoint_cache_embed_dashboard.json", "KV usage",
		"vllm:kv_cache_usage_perc", "vllm:gpu_cache_usage_perc", "sglang_token_usage")
	assertPanelMetricScope(t, "neutree_endpoint_cache_embed_dashboard.json", "Cache hit",
		"sglang_cache_hit_rate", "vllm:prefix_cache_hits_total", "vllm:prefix_cache_queries_total")
	assertPanelMetricScope(t, "neutree_endpoint_cache_embed_dashboard.json", "KV usage trend",
		"vllm:kv_cache_usage_perc", "vllm:gpu_cache_usage_perc", "sglang_token_usage")
	assertPanelMetricScope(t, "neutree_endpoint_cache_embed_dashboard.json", "Cache hit trend",
		"sglang_cache_hit_rate", "vllm:prefix_cache_hits_total", "vllm:prefix_cache_queries_total")

	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Prompt tokens/s",
		"vllm:prompt_tokens_total", "sglang_prompt_tokens_total")
	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Generation tokens/s",
		"vllm:generation_tokens_total", "sglang_generation_tokens_total", "sglang_gen_throughput")
	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Total tokens/s",
		"vllm:prompt_tokens_total", "sglang_prompt_tokens_total",
		"vllm:generation_tokens_total", "sglang_generation_tokens_total", "sglang_gen_throughput")
	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Token throughput trend",
		"vllm:prompt_tokens_total", "sglang_prompt_tokens_total",
		"vllm:generation_tokens_total", "sglang_generation_tokens_total", "sglang_gen_throughput")
	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Request Prompt Length",
		"vllm:request_prompt_tokens_bucket", "sglang_prompt_tokens_histogram_bucket")
	assertPanelMetricScope(t, "neutree_endpoint_token_latency_embed_dashboard.json", "Request Generation Length",
		"vllm:request_generation_tokens_bucket", "sglang_generation_tokens_histogram_bucket")

	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "Nodes",
		"neutree_node_ready")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "Total GPUs",
		"neutree_node_gpu_total")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "Free GPUs",
		"neutree_node_gpu_free")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "GPU avg",
		"neutree_gpu_utilization_ratio")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "VRAM avg",
		"neutree_gpu_memory_used_bytes", "neutree_gpu_memory_total_bytes")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "Utilization trend",
		"neutree_node_cpu_seconds_total", "neutree_node_memory_used_bytes", "neutree_node_memory_total_bytes",
		"neutree_gpu_utilization_ratio", "neutree_gpu_memory_used_bytes", "neutree_gpu_memory_total_bytes")
	assertPanelMetricScope(t, "neutree_cluster_overview_embed_dashboard.json", "GPU allocation details",
		"neutree_node_gpu_allocation_info", "neutree_node_gpu_hardware_info",
		"neutree_gpu_temperature_celsius")
}

func TestEndpointConsumedGPUsFallbackDoesNotAddZeroSeriesWhenProductsExist(t *testing.T) {
	expressions := dashboardPanelExpressions(t, "neutree_endpoint_overview_embed_dashboard.json", "Consumed GPUs")
	require.Len(t, expressions, 1)
	assert.Contains(t, expressions[0], "sum by (product)")
	assert.Contains(t, expressions[0], "or on() vector(0)")
	assert.NotContains(t, expressions[0], "or vector(0)")
}

func TestOverviewGPUInventoryPanelsShowTotalAndProductCounts(t *testing.T) {
	nodes := dashboardPanel(t, "neutree_overview_embed_dashboard.json", "Nodes")
	require.Len(t, nodes.Targets, 1)
	assert.Contains(t, nodes.Targets[0].Expr, "count by (node)")
	assert.Contains(t, nodes.Targets[0].Expr, "neutree_node_ready")

	total := dashboardPanel(t, "neutree_overview_embed_dashboard.json", "GPU")
	require.Len(t, total.Targets, 1)
	assert.Contains(t, total.Targets[0].Expr, "sum(last_over_time(neutree_node_gpu_total")

	models := dashboardPanel(t, "neutree_overview_embed_dashboard.json", "GPU inventory by model")
	assert.Equal(t, "table", models.Type)
	require.Len(t, models.Targets, 2)
	assert.Contains(t, models.Targets[0].Expr, "sum by (product)")
	assert.Contains(t, models.Targets[0].Expr, "neutree_node_gpu_total")
	assert.Equal(t, "table", models.Targets[0].Format)
	assert.True(t, models.Targets[0].Instant)
	assert.Equal(t, "{{product}}", models.Targets[0].LegendFormat)
	assert.Contains(t, models.Targets[1].Expr, "sum by (product)")
	assert.Contains(t, models.Targets[1].Expr, "neutree_node_gpu_free")
	assert.Equal(t, "table", models.Targets[1].Format)
	assert.True(t, models.Targets[1].Instant)
	assert.Equal(t, "{{product}}", models.Targets[1].LegendFormat)

	require.Len(t, models.Transformations, 2)
	assert.Equal(t, "merge", models.Transformations[0].ID)
	assert.Equal(t, "organize", models.Transformations[1].ID)

	organize := transformationOptions(t, models, "organize")
	assertNestedBool(t, organize, true, "excludeByName", "Time")
	assertNestedString(t, organize, "Model", "renameByName", "product")
	assertNestedString(t, organize, "Total", "renameByName", "Value")
	assertNestedString(t, organize, "Total", "renameByName", "Value #A")
	assertNestedString(t, organize, "Free", "renameByName", "Value #B")
	assertNestedFloat(t, organize, 2, "indexByName", "Value #B")
}

func TestOverviewPanelsUseFullWidthRows(t *testing.T) {
	expected := map[string]gridPosConfig{
		"Nodes":                  {H: 4, W: 6, X: 0, Y: 0},
		"GPU":                    {H: 4, W: 6, X: 6, Y: 0},
		"GPU avg":                {H: 4, W: 6, X: 12, Y: 0},
		"GPU memory":             {H: 4, W: 6, X: 18, Y: 0},
		"Current QPS":            {H: 4, W: 6, X: 0, Y: 4},
		"Avg latency":            {H: 4, W: 6, X: 6, Y: 4},
		"CPU avg":                {H: 4, W: 6, X: 12, Y: 4},
		"Memory avg":             {H: 4, W: 6, X: 18, Y: 4},
		"GPU inventory by model": {H: 5, W: 24, X: 0, Y: 8},
		"Cluster utilization":    {H: 8, W: 24, X: 0, Y: 13},
	}

	for title, gridPos := range expected {
		panel := dashboardPanel(t, "neutree_overview_embed_dashboard.json", title)
		assert.Equal(t, gridPos, panel.GridPos, "panel %q grid position", title)
	}
}

func TestEndpointOverviewDashboardShowsTrendsBeforeAllocationDetails(t *testing.T) {
	expected := map[string]gridPosConfig{
		"Consumed GPUs":          {H: 4, W: 6, X: 0, Y: 0},
		"VRAM used":              {H: 4, W: 6, X: 6, Y: 0},
		"CPU cores":              {H: 4, W: 6, X: 12, Y: 0},
		"Memory working set":     {H: 4, W: 6, X: 18, Y: 0},
		"GPU utilization trend":  {H: 8, W: 12, X: 0, Y: 4},
		"VRAM utilization trend": {H: 8, W: 12, X: 12, Y: 4},
		"CPU usage trend":        {H: 8, W: 12, X: 0, Y: 12},
		"Memory usage trend":     {H: 8, W: 12, X: 12, Y: 12},
		"GPU allocation details": {H: 10, W: 24, X: 0, Y: 20},
	}

	for title, gridPos := range expected {
		panel := dashboardPanel(t, "neutree_endpoint_overview_embed_dashboard.json", title)
		assert.Equal(t, gridPos, panel.GridPos, "panel %q grid position", title)
	}
}

func TestRatioPanelsUsePercentUnit(t *testing.T) {
	cases := []struct {
		file  string
		title string
	}{
		{"neutree_overview_embed_dashboard.json", "GPU avg"},
		{"neutree_overview_embed_dashboard.json", "GPU memory"},
		{"neutree_overview_embed_dashboard.json", "CPU avg"},
		{"neutree_overview_embed_dashboard.json", "Memory avg"},
		{"neutree_overview_embed_dashboard.json", "Cluster utilization"},
		{"neutree_cluster_overview_embed_dashboard.json", "GPU avg"},
		{"neutree_cluster_overview_embed_dashboard.json", "VRAM avg"},
		{"neutree_cluster_overview_embed_dashboard.json", "Utilization trend"},
		{"neutree_endpoint_overview_embed_dashboard.json", "GPU utilization trend"},
		{"neutree_endpoint_overview_embed_dashboard.json", "VRAM utilization trend"},
		{"neutree_endpoint_cache_embed_dashboard.json", "KV usage"},
		{"neutree_endpoint_cache_embed_dashboard.json", "Cache hit"},
		{"neutree_endpoint_cache_embed_dashboard.json", "KV usage trend"},
		{"neutree_endpoint_cache_embed_dashboard.json", "Cache hit trend"},
	}

	for _, tc := range cases {
		t.Run(tc.file+"/"+tc.title, func(t *testing.T) {
			panel := dashboardPanel(t, tc.file, tc.title)
			assertNestedString(t, panel.FieldConfig, "percentunit", "defaults", "unit")
		})
	}
}

func TestEndpointOverviewResourcePanelsUseExpectedUnits(t *testing.T) {
	cases := []struct {
		title string
		unit  string
	}{
		{"Consumed GPUs", "none"},
		{"VRAM used", "bytes"},
		{"CPU cores", "cores"},
		{"Memory working set", "bytes"},
		{"CPU usage trend", "cores"},
		{"Memory usage trend", "bytes"},
	}

	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			panel := dashboardPanel(t, "neutree_endpoint_overview_embed_dashboard.json", tc.title)
			assertNestedString(t, panel.FieldConfig, tc.unit, "defaults", "unit")
		})
	}
}

func TestSplitDashboardKpiRowsUseFullWidth(t *testing.T) {
	for _, file := range splitDashboardFiles {
		t.Run(file, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Clean(file))
			require.NoError(t, err)

			var dashboard struct {
				Panels []panelConfig `json:"panels"`
			}
			require.NoError(t, json.Unmarshal(raw, &dashboard))

			widthByY := map[int]int{}
			for _, panel := range dashboard.Panels {
				if panel.Type != "stat" {
					continue
				}
				widthByY[panel.GridPos.Y] += panel.GridPos.W
			}

			for y, width := range widthByY {
				assert.Equalf(t, 24, width, "%s stat row y=%d should fill the dashboard width", file, y)
			}
		})
	}
}

func TestEndpointQueueDashboardUsesStatusAndQueueTimePanels(t *testing.T) {
	expected := map[string]gridPosConfig{
		"Running":                   {H: 4, W: 6, X: 0, Y: 0},
		"Waiting":                   {H: 4, W: 6, X: 6, Y: 0},
		"Preemptions / Retractions": {H: 4, W: 6, X: 12, Y: 0},
		"Queue time P95":            {H: 4, W: 6, X: 18, Y: 0},
		"Queue status trend":        {H: 8, W: 12, X: 0, Y: 4},
		"Queue time trend":          {H: 8, W: 12, X: 12, Y: 4},
	}

	for title, gridPos := range expected {
		panel := dashboardPanel(t, "neutree_endpoint_queue_embed_dashboard.json", title)
		assert.Equal(t, gridPos, panel.GridPos, "panel %q grid position", title)
	}

	expressions := strings.Join(
		dashboardPanelExpressions(t, "neutree_endpoint_queue_embed_dashboard.json", "Queue status trend"),
		"\n",
	)
	assert.NotContains(t, expressions, "vllm:num_requests_swapped")
	assert.Contains(t, expressions, "vllm:num_preemptions_total")
	assert.Contains(t, expressions, "sglang_num_retracted_requests_total")

	panel := dashboardPanel(t, "neutree_endpoint_queue_embed_dashboard.json", "Queue status trend")
	assert.Equal(t, "{{replica}} preemptions/s", panel.Targets[2].LegendFormat)
	assert.Equal(t, "{{replica}} retractions/s", panel.Targets[3].LegendFormat)
}

func TestEndpointCacheCapacityUsesEngineTokenCapacity(t *testing.T) {
	panel := dashboardPanel(t, "neutree_endpoint_cache_embed_dashboard.json", "KV token capacity")
	require.Len(t, panel.Targets, 1)

	expr := panel.Targets[0].Expr
	assert.Contains(t, expr, "vllm:cache_config_info")
	assert.Contains(t, expr, `label_value(last_over_time(vllm:cache_config_info`)
	assert.Contains(t, expr, `"kv_cache_size_tokens"`)
	assert.Contains(t, expr, `"num_gpu_blocks"`)
	assert.Contains(t, expr, `"block_size"`)
	assert.Contains(t, expr, "sglang_max_total_num_tokens")
	assert.NotContains(t, expr, "vector(0)")
	assert.NotContains(t, expr, "vllm:neutree_kv_token_capacity")
	assert.NotContains(t, expr, "vllm:num_gpu_blocks")
	assert.NotContains(t, expr, "vllm:num_total_gpu_blocks")
	assertNestedString(t, panel.FieldConfig, "none", "defaults", "unit")
}

func TestClusterOverviewUtilizationTrendDoesNotMixPendingReplicas(t *testing.T) {
	panel := dashboardPanel(t, "neutree_cluster_overview_embed_dashboard.json", "Utilization trend")
	require.Len(t, panel.Targets, 4)

	expressions := strings.Join(dashboardPanelExpressions(t, "neutree_cluster_overview_embed_dashboard.json", "Utilization trend"), "\n")
	assert.Contains(t, expressions, "neutree_node_cpu_seconds_total")
	assert.Contains(t, expressions, "neutree_node_memory_used_bytes")
	assert.Contains(t, expressions, "neutree_node_memory_total_bytes")
	assert.Contains(t, expressions, "neutree_gpu_utilization_ratio")
	assert.Contains(t, expressions, "neutree_gpu_memory_used_bytes")
	assert.NotContains(t, expressions, "neutree_endpoint_pending_replicas")
}

func TestOverviewCPUAndMemoryPanelsUseNeutreeMetricsOnly(t *testing.T) {
	for _, panelTitle := range []string{"CPU avg", "Memory avg", "Cluster utilization"} {
		t.Run(panelTitle, func(t *testing.T) {
			expressions := strings.Join(dashboardPanelExpressions(t, "neutree_overview_embed_dashboard.json", panelTitle), "\n")
			if panelTitle != "Memory avg" {
				assert.Contains(t, expressions, "neutree_node_cpu_seconds_total")
				assert.NotContains(t, expressions, "rate(node_cpu_seconds_total")
			}
			if panelTitle != "CPU avg" {
				assert.Contains(t, expressions, "neutree_node_memory_used_bytes")
				assert.Contains(t, expressions, "neutree_node_memory_total_bytes")
			}
			assert.NotContains(t, expressions, "node_memory_MemAvailable_bytes")
			assert.NotContains(t, expressions, "node_memory_MemTotal_bytes")
		})
	}
}

func TestEndpointLatencyTrendsSplitByLatencyDimension(t *testing.T) {
	cases := []struct {
		title    string
		metric   string
		gridPos  gridPosConfig
		fallback string
	}{
		{
			title:    "E2E latency trend",
			metric:   "vllm:e2e_request_latency_seconds_bucket",
			gridPos:  gridPosConfig{H: 8, W: 12, X: 0, Y: 4},
			fallback: "sglang_e2e_request_latency_seconds_bucket",
		},
		{
			title:    "TTFT latency trend",
			metric:   "vllm:time_to_first_token_seconds_bucket",
			gridPos:  gridPosConfig{H: 8, W: 12, X: 12, Y: 4},
			fallback: "sglang_time_to_first_token_seconds_bucket",
		},
		{
			title:    "TPOT latency trend",
			metric:   "vllm:request_time_per_output_token_seconds_bucket",
			gridPos:  gridPosConfig{H: 8, W: 12, X: 0, Y: 12},
			fallback: "vllm:time_per_output_token_seconds_bucket",
		},
		{
			title:    "ITL latency trend",
			metric:   "vllm:inter_token_latency_seconds_bucket",
			gridPos:  gridPosConfig{H: 8, W: 12, X: 12, Y: 12},
			fallback: "sglang_inter_token_latency_seconds_bucket",
		},
	}

	for _, tc := range cases {
		t.Run(tc.title, func(t *testing.T) {
			panel := dashboardPanel(t, "neutree_endpoint_latency_embed_dashboard.json", tc.title)
			assert.Equal(t, "timeseries", panel.Type)
			assert.Equal(t, tc.gridPos, panel.GridPos)
			require.Len(t, panel.Targets, 4)

			expected := map[string]string{
				"P50": "histogram_quantile(0.5,",
				"P90": "histogram_quantile(0.9,",
				"P95": "histogram_quantile(0.95,",
				"P99": "histogram_quantile(0.99,",
			}

			for _, target := range panel.Targets {
				quantile, ok := expected[target.LegendFormat]
				require.True(t, ok, "unexpected latency legend %q", target.LegendFormat)
				assert.Contains(t, target.Expr, quantile)
				assert.Contains(t, target.Expr, tc.metric)
				if tc.fallback != "" {
					assert.Contains(t, target.Expr, tc.fallback)
				}
				assert.Contains(t, target.Expr, `neutree_cluster=~"$Cluster"`)
				assert.Contains(t, target.Expr, `application=~".*$Endpoint.*"`)
				delete(expected, target.LegendFormat)
			}

			assert.Empty(t, expected)
		})
	}
}

func TestClusterOverviewGPUAllocationDetailsUsesReplicaScopedAllocationRows(t *testing.T) {
	panel := dashboardPanel(t, "neutree_cluster_overview_embed_dashboard.json", "GPU allocation details")
	require.NotEmpty(t, panel.Targets)

	allocationTarget := panel.Targets[0]
	assert.Equal(t, "A", allocationTarget.RefID)
	assert.Contains(t, allocationTarget.Expr, "neutree_node_gpu_allocation_info")
	assert.Contains(t, allocationTarget.Expr, "max by (endpoint_replica, endpoint, replica, node, node_gpu, gpu_uuid, gpu_index, product, physical_vram, vram)")
	assert.NotContains(t, allocationTarget.Expr, "neutree_node_gpu_allocation{")
	assert.NotContains(t, allocationTarget.Expr, "unless on (node, gpu_uuid)")
	assert.NotContains(t, allocationTarget.Expr, `"endpoint", "-"`)
	assert.NotContains(t, allocationTarget.Expr, `"replica", "-"`)

	organize := transformationOptions(t, panel, "organize")
	assertNestedString(t, organize, "Endpoint / Replica", "renameByName", "endpoint_replica")
	assertNestedBool(t, organize, true, "excludeByName", "gpu_uuid")
}

func TestEndpointTokenLatencyLengthHeatmapsMatchVLLMDisplay(t *testing.T) {
	assertTokenLengthHeatmap(
		t,
		"neutree_endpoint_token_latency_embed_dashboard.json",
		"Request Prompt Length",
		"Heatmap of request prompt length",
		"Prompt Length",
		"vllm:request_prompt_tokens_bucket",
		"sglang_prompt_tokens_histogram_bucket",
	)
	assertTokenLengthHeatmap(
		t,
		"neutree_endpoint_token_latency_embed_dashboard.json",
		"Request Generation Length",
		"Heatmap of request generation length",
		"Generation Length",
		"vllm:request_generation_tokens_bucket",
		"sglang_generation_tokens_histogram_bucket",
	)
}

func TestGPUAllocationDetailTablesMergeAndOrganizeDeviceFields(t *testing.T) {
	assertGPUAllocationDetailTable(t, "neutree_cluster_overview_embed_dashboard.json", false, false)
	assertGPUAllocationDetailTable(t, "neutree_endpoint_overview_embed_dashboard.json", true, true)
}

func TestGPUAllocationDetailVisibleColumns(t *testing.T) {
	tests := []struct {
		file            string
		expectedColumns []string
	}{
		{
			file: "neutree_cluster_overview_embed_dashboard.json",
			expectedColumns: []string{
				"Endpoint / Replica",
				"Node / GPU",
				"Physical VRAM Used / Total",
				"VRAM Used / Allocated",
				"GPU Util",
				"Product",
				"Architecture",
				"CUDA Capability",
				"Driver",
				"CUDA Driver",
				"Temperature",
				"NVLink",
				"NVSwitch",
				"PCIe Bus",
				"PCIe Gen",
				"PCIe Width",
				"NUMA",
			},
		},
		{
			file: "neutree_endpoint_overview_embed_dashboard.json",
			expectedColumns: []string{
				"Model / Replica",
				"Node / GPU",
				"Physical VRAM Used / Total",
				"VRAM Used / Allocated",
				"GPU Util",
				"Product",
				"Architecture",
				"CUDA Capability",
				"Driver",
				"CUDA Driver",
				"Temperature",
				"NVLink",
				"NVSwitch",
				"PCIe Bus",
				"PCIe Gen",
				"PCIe Width",
				"NUMA",
			},
		},
	}

	for _, tc := range tests {
		t.Run(tc.file, func(t *testing.T) {
			panel := dashboardPanel(t, tc.file, "GPU allocation details")
			organize := transformationOptions(t, panel, "organize")

			assert.Equal(t, tc.expectedColumns, organizedVisibleColumnNames(t, organize))
		})
	}
}

func TestEndpointGPUAllocationDetailTablesMatchClusterDisplay(t *testing.T) {
	clusterPanel := dashboardPanel(t, "neutree_cluster_overview_embed_dashboard.json", "GPU allocation details")
	clusterOrganize := transformationOptions(t, clusterPanel, "organize")

	for _, file := range []string{
		"neutree_endpoint_overview_embed_dashboard.json",
	} {
		t.Run(file, func(t *testing.T) {
			panel := dashboardPanel(t, file, "GPU allocation details")
			organize := transformationOptions(t, panel, "organize")

			expectedIndexByName := map[string]any{}
			for name, index := range clusterOrganize["indexByName"].(map[string]any) {
				expectedIndexByName[name] = index
			}
			delete(expectedIndexByName, "endpoint_replica")
			expectedIndexByName["model_replica"] = float64(0)
			expectedIndexByName["node_gpu"] = float64(1)
			expectedIndexByName["physical_vram"] = float64(2)
			expectedIndexByName["vram"] = float64(3)
			expectedIndexByName["Value #C"] = float64(4)
			expectedIndexByName["product"] = float64(5)
			expectedIndexByName["architecture"] = float64(6)
			expectedIndexByName["cuda_capability"] = float64(7)
			expectedIndexByName["driver_version"] = float64(8)
			expectedIndexByName["cuda_driver_version"] = float64(9)
			expectedIndexByName["Value #D"] = float64(10)
			expectedIndexByName["nvlink"] = float64(11)
			expectedIndexByName["nvswitch"] = float64(12)
			expectedIndexByName["pcie_bus_id"] = float64(13)
			expectedIndexByName["pcie_generation"] = float64(14)
			expectedIndexByName["pcie_width"] = float64(15)
			expectedIndexByName["numa_node"] = float64(16)
			assert.Equal(t, expectedIndexByName, organize["indexByName"])
			assertNestedBool(t, organize, true, "excludeByName", "endpoint")
			assertNestedBool(t, organize, true, "excludeByName", "replica")

			expectedRenameByName := map[string]any{}
			for name, rename := range clusterOrganize["renameByName"].(map[string]any) {
				expectedRenameByName[name] = rename
			}
			delete(expectedRenameByName, "endpoint_replica")
			expectedRenameByName["model_replica"] = "Model / Replica"
			assert.Equal(t, expectedRenameByName, organize["renameByName"])
			assertGPUAllocationSharedFieldConfig(t, clusterPanel, panel)
			require.Len(t, panel.Targets, len(clusterPanel.Targets)+1)
			assertTableSortBy(t, clusterPanel, "Node / GPU")
			assertTableSortBy(t, panel, "Model / Replica")

			expressions := strings.Join(dashboardPanelExpressions(t, file, "GPU allocation details"), "\n")
			assert.NotContains(t, expressions, "pod")
			assert.NotContains(t, expressions, "engine_version")
			assert.Contains(t, expressions, "model_name")
			assert.Contains(t, expressions, "application")
			assertPanelDoesNotExposeFields(t, panel, "engine", "engine_version", "Engine", "Engine Version")
		})
	}
}

func TestEndpointGPUTrendsUseGPULevelLegendWithRealReplicaName(t *testing.T) {
	for _, title := range []string{"GPU utilization trend", "VRAM utilization trend"} {
		t.Run(title, func(t *testing.T) {
			panel := dashboardPanel(t, "neutree_endpoint_overview_embed_dashboard.json", title)
			require.Len(t, panel.Targets, 1)
			assert.True(t, strings.HasPrefix(panel.Targets[0].LegendFormat, "replica-{{replica}}"))
			assert.NotContains(t, strings.ToLower(panel.Targets[0].LegendFormat), "order")
			assert.Equal(t, "replica-{{replica}}-node-{{node}}-gpu-{{gpu_index}}", panel.Targets[0].LegendFormat)
			assert.Contains(t, panel.Targets[0].Expr, "group_left(replica, gpu_index)")
			assert.Contains(t, panel.Targets[0].Expr, "max by (node, gpu_uuid, replica, gpu_index)")
		})
	}
}

func organizedVisibleColumnNames(t *testing.T, organize map[string]any) []string {
	t.Helper()

	indexByName, ok := organize["indexByName"].(map[string]any)
	require.True(t, ok)
	renameByName, ok := organize["renameByName"].(map[string]any)
	require.True(t, ok)
	excludeByName, ok := organize["excludeByName"].(map[string]any)
	require.True(t, ok)

	type column struct {
		name  string
		index float64
	}
	columns := make([]column, 0, len(indexByName))
	for field, indexValue := range indexByName {
		if hidden, _ := excludeByName[field].(bool); hidden {
			continue
		}

		index, ok := indexValue.(float64)
		require.True(t, ok)
		displayName, ok := renameByName[field].(string)
		require.True(t, ok, "missing rename for %s", field)
		columns = append(columns, column{name: displayName, index: index})
	}

	sort.Slice(columns, func(i, j int) bool {
		return columns[i].index < columns[j].index
	})

	names := make([]string, 0, len(columns))
	for _, column := range columns {
		names = append(names, column.name)
	}

	return names
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

func assertDashboardPanelTitles(t *testing.T, file string, titles ...string) {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)

	var dashboard struct {
		Panels []struct {
			Title string `json:"title"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(raw, &dashboard))

	actual := make([]string, 0, len(dashboard.Panels))
	for _, panel := range dashboard.Panels {
		actual = append(actual, panel.Title)
	}

	assert.Equal(t, titles, actual)
}

func isTablePanel(panel panelConfig) bool {
	if panel.Type == "table" {
		return true
	}
	for _, target := range panel.Targets {
		if target.Format == "table" {
			return true
		}
	}

	return false
}

func organizeHidesField(organize map[string]any, field string) bool {
	excludeByName, ok := organize["excludeByName"].(map[string]any)
	if !ok {
		return false
	}
	hidden, ok := excludeByName[field].(bool)

	return ok && hidden
}

func organizeRenamesValueField(organize map[string]any, field string) bool {
	renameByName, ok := organize["renameByName"].(map[string]any)
	if !ok {
		return false
	}
	renamed, ok := renameByName[field].(string)

	return ok && renamed != "" && !strings.HasPrefix(renamed, "Value")
}

func assertPanelMetricScope(t *testing.T, file, title string, metrics ...string) {
	t.Helper()

	expressions := dashboardPanelExpressions(t, file, title)
	require.NotEmpty(t, expressions)

	joined := strings.Join(expressions, "\n")
	for _, metric := range metrics {
		assert.Contains(t, joined, metric)
	}
}

func dashboardPanelExpressions(t *testing.T, file, title string) []string {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)

	var dashboard struct {
		Panels []struct {
			Title   string `json:"title"`
			Targets []struct {
				Expr string `json:"expr"`
			} `json:"targets"`
		} `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(raw, &dashboard))

	for _, panel := range dashboard.Panels {
		if panel.Title != title {
			continue
		}

		expressions := make([]string, 0, len(panel.Targets))
		for _, target := range panel.Targets {
			if target.Expr != "" {
				expressions = append(expressions, target.Expr)
			}
		}

		return expressions
	}

	t.Fatalf("panel %q not found in %s", title, file)

	return nil
}

func assertTokenLengthHeatmap(t *testing.T, file, title, description, yAxisLabel string, metrics ...string) {
	t.Helper()

	panel := dashboardPanel(t, file, title)
	assert.Equal(t, "heatmap", panel.Type)
	assert.Equal(t, description, panel.Description)

	require.NotEmpty(t, panel.FieldConfig)
	assertNestedString(t, panel.FieldConfig, "linear", "defaults", "custom", "scaleDistribution", "type")
	assertNestedBool(t, panel.FieldConfig, false, "defaults", "custom", "hideFrom", "legend")
	assertNestedBool(t, panel.FieldConfig, false, "defaults", "custom", "hideFrom", "tooltip")
	assertNestedBool(t, panel.FieldConfig, false, "defaults", "custom", "hideFrom", "viz")

	require.NotEmpty(t, panel.Options)
	assertNestedBool(t, panel.Options, false, "calculate")
	assertNestedFloat(t, panel.Options, 1, "cellGap")
	assertNestedString(t, panel.Options, "none", "cellValues", "unit")
	assertNestedString(t, panel.Options, "scheme", "color", "mode")
	assertNestedString(t, panel.Options, "exponential", "color", "scale")
	assertNestedString(t, panel.Options, "Spectral", "color", "scheme")
	assertNestedFloat(t, panel.Options, 0.5, "color", "exponent")
	assertNestedFloat(t, panel.Options, 64, "color", "steps")
	assertNestedFloat(t, panel.Options, 1e-9, "filterValues", "le")
	assertNestedBool(t, panel.Options, true, "legend", "show")
	assertNestedString(t, panel.Options, "Request count", "rowsFrame", "value")
	assertNestedString(t, panel.Options, "single", "tooltip", "mode")
	assertNestedBool(t, panel.Options, true, "tooltip", "yHistogram")
	assertNestedString(t, panel.Options, yAxisLabel, "yAxis", "axisLabel")
	assertNestedString(t, panel.Options, "none", "yAxis", "unit")

	require.Len(t, panel.Targets, 1)
	target := panel.Targets[0]
	assert.Equal(t, "heatmap", target.Format)
	assert.False(t, target.Instant)
	assert.True(t, target.Range)
	assert.Equal(t, "{{le}}", target.LegendFormat)
	for _, metric := range metrics {
		assert.Contains(t, target.Expr, metric)
	}
	assert.Contains(t, target.Expr, `neutree_cluster=~"$Cluster"`)
	assert.Contains(t, target.Expr, `application=~".*$Endpoint.*"`)
	assert.NotContains(t, target.Expr, "or vector(0)")
}

func assertGPUAllocationDetailTable(t *testing.T, file string, endpointScoped bool, withRuntimeModel bool) {
	t.Helper()

	panel := dashboardPanel(t, file, "GPU allocation details")
	assert.Equal(t, 10, panel.GridPos.H)
	assert.Equal(t, 24, panel.GridPos.W)
	assert.Equal(t, "table", panel.Type)
	assertNestedBool(t, panel.Options, true, "showHeader")
	assertNestedString(t, panel.Options, "md", "cellHeight")
	assertNestedBool(t, panel.Options, false, "footer", "show")

	expectedTargets := 4
	if withRuntimeModel {
		expectedTargets = 5
	}
	require.Len(t, panel.Targets, expectedTargets)
	for _, target := range panel.Targets {
		assert.Equal(t, "table", target.Format)
		assert.True(t, target.Instant)
		assert.NotEmpty(t, target.Expr)
		if endpointScoped {
			assert.Contains(t, target.Expr, `endpoint=~"$Endpoint"`)
		} else {
			assert.NotContains(t, target.Expr, `endpoint=~"$Endpoint"`)
			assert.Contains(t, target.Expr, `endpoint!="-"`)
			assert.Contains(t, target.Expr, `replica!="-"`)
		}
	}

	assert.Equal(t, "A", panel.Targets[0].RefID)
	assert.Contains(t, panel.Targets[0].Expr, "neutree_node_gpu_allocation_info")
	assert.Contains(t, panel.Targets[0].Expr, "max by (endpoint_replica, endpoint, replica, node, node_gpu, gpu_uuid, gpu_index, product, physical_vram, vram)")
	assert.Contains(t, panel.Targets[1].Expr, "neutree_node_gpu_hardware_info")
	assert.Contains(t, panel.Targets[1].Expr, "group_left(architecture")
	assert.NotContains(t, panel.Targets[1].Expr, "memory_total_mib")
	assert.NotContains(t, panel.Targets[1].Expr, "last_over_time(neutree_node_gpu_hardware_info")
	assert.Contains(t, panel.Targets[2].Expr, "neutree_gpu_utilization_ratio")
	assert.NotContains(t, panel.Targets[2].Expr, "DCGM_FI_DEV_GPU_UTIL")
	assert.Contains(t, panel.Targets[3].Expr, "neutree_gpu_temperature_celsius")
	assert.NotContains(t, panel.Targets[3].Expr, "DCGM_FI_DEV_GPU_TEMP")
	if withRuntimeModel {
		assert.Contains(t, panel.Targets[4].Expr, `max by (replica, model_name)`)
		assert.Contains(t, panel.Targets[4].Expr, "vllm:engine_sleep_state")
		assert.Contains(t, panel.Targets[4].Expr, `model_name=~".+"`)
		assert.Contains(t, panel.Targets[4].Expr, `served_model_name=~".+"`)
		assert.Contains(t, panel.Targets[4].Expr, `label_replace`)
		assert.Contains(t, panel.Targets[4].Expr, `label_set`)
		assert.Contains(t, panel.Targets[4].Expr, "unless on (replica)")
		assert.Contains(t, panel.Targets[4].Expr, `model_replica`)
		assert.Contains(t, panel.Targets[4].Expr, `application`)
		assert.Contains(t, panel.Targets[4].Expr, "group_left(model_name)")
	}

	assertTransformationIDs(t, panel, "merge", "organize")
	organize := transformationOptions(t, panel, "organize")
	assertNestedBool(t, organize, true, "excludeByName", "Value")
	assertNestedBool(t, organize, true, "excludeByName", "Value #A")
	assertNestedBool(t, organize, true, "excludeByName", "Value #B")
	assertNestedBool(t, organize, true, "excludeByName", "gpu_index")
	assertNestedBool(t, organize, true, "excludeByName", "gpu_uuid")
	assertNestedBool(t, organize, true, "excludeByName", "endpoint")
	assertNestedBool(t, organize, true, "excludeByName", "replica")
	assertNestedString(t, organize, "Node / GPU", "renameByName", "node_gpu")
	assertNestedMissing(t, organize, "renameByName", "gpu_uuid")
	assertNestedString(t, organize, "CUDA Capability", "renameByName", "cuda_capability")
	assertNestedString(t, organize, "Driver", "renameByName", "driver_version")
	assertNestedString(t, organize, "CUDA Driver", "renameByName", "cuda_driver_version")
	assertNestedMissing(t, organize, "renameByName", "memory_total_mib")
	assertNestedString(t, organize, "Physical VRAM Used / Total", "renameByName", "physical_vram")
	assertNestedString(t, organize, "VRAM Used / Allocated", "renameByName", "vram")
	assertNestedString(t, organize, "GPU Util", "renameByName", "Value #C")
	assertNestedString(t, organize, "Temperature", "renameByName", "Value #D")
	assertNestedString(t, organize, "PCIe Bus", "renameByName", "pcie_bus_id")
	assertNestedString(t, organize, "PCIe Gen", "renameByName", "pcie_generation")
	assertNestedString(t, organize, "PCIe Width", "renameByName", "pcie_width")
	assertNestedString(t, organize, "NUMA", "renameByName", "numa_node")
	assertGPUAllocationDetailColumnOrder(t, organize, endpointScoped, withRuntimeModel)

	if withRuntimeModel {
		assertNestedMissing(t, organize, "excludeByName", "Value #E")
		assertNestedMissing(t, organize, "excludeByName", "Value #F")
		assertNestedMissing(t, organize, "renameByName", "engine")
		assertNestedMissing(t, organize, "renameByName", "engine_version")
		assertNestedString(t, organize, "Model / Replica", "renameByName", "model_replica")
	} else {
		assertNestedString(t, organize, "Endpoint / Replica", "renameByName", "endpoint_replica")
	}

	assertFieldOverrideUnit(t, panel, "GPU Util", "percentunit")
	assertFieldOverrideUnit(t, panel, "Temperature", "celsius")
	assertFieldOverrideValueMapping(t, panel, "NVLink", "unknown", "-")
	assertFieldOverrideValueMapping(t, panel, "NVSwitch", "unknown", "-")
	if endpointScoped {
		assertFieldOverrideWidth(t, panel, "Model / Replica", 260)
		assertFieldOverrideWrapText(t, panel, "Model / Replica")
	} else {
		assertFieldOverrideWidth(t, panel, "Endpoint / Replica", 220)
		assertFieldOverrideWrapText(t, panel, "Endpoint / Replica")
	}
	assertFieldOverrideWidth(t, panel, "Node / GPU", 190)
	assertFieldOverrideWrapText(t, panel, "Node / GPU")
	assertFieldOverrideWidth(t, panel, "Physical VRAM Used / Total", 180)
	assertFieldOverrideWrapText(t, panel, "Physical VRAM Used / Total")
	assertFieldOverrideWidth(t, panel, "VRAM Used / Allocated", 180)
	assertFieldOverrideWrapText(t, panel, "VRAM Used / Allocated")
	if !endpointScoped {
		assertFieldOverrideWidth(t, panel, "Engine Version", 160)
		assertFieldOverrideWrapText(t, panel, "Engine Version")
	}
	assertFieldOverrideWidth(t, panel, "PCIe Bus", 180)
	assertFieldOverrideWrapText(t, panel, "PCIe Bus")
	if endpointScoped {
		assertTableSortBy(t, panel, "Model / Replica")
	} else {
		assertTableSortBy(t, panel, "Node / GPU")
	}
}

func assertGPUAllocationDetailColumnOrder(
	t *testing.T,
	organize map[string]any,
	endpointScoped bool,
	withRuntimeModel bool,
) {
	t.Helper()

	expectedPrefix := map[string]int{
		"endpoint_replica":    0,
		"node_gpu":            1,
		"physical_vram":       2,
		"vram":                3,
		"Value #C":            4,
		"product":             5,
		"architecture":        6,
		"cuda_capability":     7,
		"driver_version":      8,
		"cuda_driver_version": 9,
		"Value #D":            10,
		"nvlink":              11,
		"nvswitch":            12,
		"pcie_bus_id":         13,
		"pcie_generation":     14,
		"pcie_width":          15,
		"numa_node":           16,
	}
	if endpointScoped && withRuntimeModel {
		delete(expectedPrefix, "endpoint_replica")
		expectedPrefix["model_replica"] = 0
		expectedPrefix["node_gpu"] = 1
		expectedPrefix["physical_vram"] = 2
		expectedPrefix["vram"] = 3
		expectedPrefix["Value #C"] = 4
		expectedPrefix["product"] = 5
		expectedPrefix["architecture"] = 6
		expectedPrefix["cuda_capability"] = 7
		expectedPrefix["driver_version"] = 8
		expectedPrefix["cuda_driver_version"] = 9
		expectedPrefix["Value #D"] = 10
		expectedPrefix["nvlink"] = 11
		expectedPrefix["nvswitch"] = 12
		expectedPrefix["pcie_bus_id"] = 13
		expectedPrefix["pcie_generation"] = 14
		expectedPrefix["pcie_width"] = 15
		expectedPrefix["numa_node"] = 16
	}
	for name, index := range expectedPrefix {
		assertNestedFloat(t, organize, float64(index), "indexByName", name)
	}
	assertNestedMissing(t, organize, "indexByName", "gpu_uuid")
	assertNestedMissing(t, organize, "indexByName", "memory_total_mib")

	if withRuntimeModel {
		assertNestedMissing(t, organize, "indexByName", "engine")
		assertNestedMissing(t, organize, "indexByName", "engine_version")
		assertNestedFloat(t, organize, 0, "indexByName", "model_replica")

		return
	}
}

func assertGPUAllocationSharedFieldConfig(t *testing.T, clusterPanel, endpointPanel panelConfig) {
	t.Helper()

	assert.Equal(t, clusterPanel.FieldConfig["defaults"], endpointPanel.FieldConfig["defaults"])
	for _, field := range []string{
		"GPU Util",
		"Temperature",
		"NVLink",
		"NVSwitch",
		"Node / GPU",
		"Physical VRAM Used / Total",
		"VRAM Used / Allocated",
		"PCIe Bus",
	} {
		assert.Equal(t, fieldOverrideProperties(t, clusterPanel, field), fieldOverrideProperties(t, endpointPanel, field))
	}
	assert.Equal(t, float64(220), fieldOverrideProperties(t, clusterPanel, "Endpoint / Replica")["custom.width"])
	assert.Equal(t, float64(260), fieldOverrideProperties(t, endpointPanel, "Model / Replica")["custom.width"])
}

func assertPanelDoesNotExposeFields(t *testing.T, panel panelConfig, fields ...string) {
	t.Helper()

	organize := transformationOptions(t, panel, "organize")
	for _, field := range fields {
		assert.NotContains(t, organizedVisibleColumnNames(t, organize), field)
		assertNestedMissing(t, organize, "indexByName", field)
		assertNestedMissing(t, organize, "renameByName", field)
		assertNoFieldOverride(t, panel, field)
	}
}

func assertNoFieldOverride(t *testing.T, panel panelConfig, field string) {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)
	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)
		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		assert.NotEqual(t, field, matcher["options"])
	}
}

func fieldOverrideProperties(t *testing.T, panel panelConfig, field string) map[string]any {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)
	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)
		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		if matcher["options"] != field {
			continue
		}

		properties, ok := override["properties"].([]any)
		require.True(t, ok)
		result := map[string]any{}
		for _, propertyItem := range properties {
			property, ok := propertyItem.(map[string]any)
			require.True(t, ok)
			id, ok := property["id"].(string)
			require.True(t, ok)
			result[id] = property["value"]
		}

		return result
	}

	t.Fatalf("field override for %q not found", field)

	return nil
}

func assertTransformationIDs(t *testing.T, panel panelConfig, ids ...string) {
	t.Helper()

	actual := make([]string, 0, len(panel.Transformations))
	for _, transformation := range panel.Transformations {
		actual = append(actual, transformation.ID)
	}

	assert.Equal(t, ids, actual)
}

func transformationOptions(t *testing.T, panel panelConfig, id string) map[string]any {
	t.Helper()

	for _, transformation := range panel.Transformations {
		if transformation.ID == id {
			require.NotNil(t, transformation.Options)

			return transformation.Options
		}
	}

	t.Fatalf("transformation %q not found", id)

	return nil
}

func assertTableSortBy(t *testing.T, panel panelConfig, fields ...string) {
	t.Helper()

	sortBy, ok := panel.Options["sortBy"].([]any)
	require.True(t, ok)
	require.Len(t, sortBy, len(fields))

	for i, field := range fields {
		item, ok := sortBy[i].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, field, item["id"])
		assert.Equal(t, field, item["displayName"])
		assert.Equal(t, false, item["desc"])
	}
}

func assertFieldOverrideUnit(t *testing.T, panel panelConfig, field, unit string) {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)

	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)

		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		if matcher["options"] != field {
			continue
		}

		properties, ok := override["properties"].([]any)
		require.True(t, ok)
		for _, propertyItem := range properties {
			property, ok := propertyItem.(map[string]any)
			require.True(t, ok)
			if property["id"] == "unit" {
				assert.Equal(t, unit, property["value"])

				return
			}
		}
	}

	t.Fatalf("unit override for field %q not found", field)
}

func assertFieldOverrideWidth(t *testing.T, panel panelConfig, field string, width float64) {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)

	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)

		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		if matcher["options"] != field {
			continue
		}

		properties, ok := override["properties"].([]any)
		require.True(t, ok)
		for _, propertyItem := range properties {
			property, ok := propertyItem.(map[string]any)
			require.True(t, ok)
			if property["id"] == "custom.width" {
				assert.Equal(t, width, property["value"])

				return
			}
		}
	}

	t.Fatalf("width override for field %q not found", field)
}

func assertFieldOverrideWrapText(t *testing.T, panel panelConfig, field string) {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)

	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)

		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		if matcher["options"] != field {
			continue
		}

		properties, ok := override["properties"].([]any)
		require.True(t, ok)
		for _, propertyItem := range properties {
			property, ok := propertyItem.(map[string]any)
			require.True(t, ok)
			if property["id"] == "custom.wrapText" {
				assert.Equal(t, true, property["value"])

				return
			}
		}
	}

	t.Fatalf("wrap text override for field %q not found", field)
}

func assertFieldOverrideValueMapping(t *testing.T, panel panelConfig, field, value, text string) {
	t.Helper()

	overrides, ok := panel.FieldConfig["overrides"].([]any)
	require.True(t, ok)

	for _, item := range overrides {
		override, ok := item.(map[string]any)
		require.True(t, ok)

		matcher, ok := override["matcher"].(map[string]any)
		require.True(t, ok)
		if matcher["options"] != field {
			continue
		}

		properties, ok := override["properties"].([]any)
		require.True(t, ok)
		for _, propertyItem := range properties {
			property, ok := propertyItem.(map[string]any)
			require.True(t, ok)
			if property["id"] != "mappings" {
				continue
			}

			mappings, ok := property["value"].([]any)
			require.True(t, ok)
			for _, mappingItem := range mappings {
				mapping, ok := mappingItem.(map[string]any)
				require.True(t, ok)

				options, ok := mapping["options"].(map[string]any)
				require.True(t, ok)
				option, ok := options[value].(map[string]any)
				if !ok {
					continue
				}

				assert.Equal(t, text, option["text"])

				return
			}
		}
	}

	t.Fatalf("value mapping override for field %q value %q not found", field, value)
}

func dashboardPanel(t *testing.T, file, title string) panelConfig {
	t.Helper()

	raw, err := os.ReadFile(filepath.Clean(file))
	require.NoError(t, err)

	var dashboard struct {
		Panels []panelConfig `json:"panels"`
	}
	require.NoError(t, json.Unmarshal(raw, &dashboard))

	for _, panel := range dashboard.Panels {
		if panel.Title == title {
			return panel
		}
	}

	t.Fatalf("panel %q not found in %s", title, file)

	return panelConfig{}
}

func assertNestedString(t *testing.T, root map[string]any, expected string, keys ...string) {
	t.Helper()

	actual, ok := nestedValue(t, root, keys...).(string)
	require.True(t, ok, "expected %s to be a string", strings.Join(keys, "."))
	assert.Equal(t, expected, actual)
}

func assertNestedBool(t *testing.T, root map[string]any, expected bool, keys ...string) {
	t.Helper()

	actual, ok := nestedValue(t, root, keys...).(bool)
	require.True(t, ok, "expected %s to be a bool", strings.Join(keys, "."))
	assert.Equal(t, expected, actual)
}

func assertNestedFloat(t *testing.T, root map[string]any, expected float64, keys ...string) {
	t.Helper()

	actual, ok := nestedValue(t, root, keys...).(float64)
	require.True(t, ok, "expected %s to be a number", strings.Join(keys, "."))
	assert.Equal(t, expected, actual)
}

func assertNestedMissing(t *testing.T, root map[string]any, keys ...string) {
	t.Helper()

	var current any = root
	for index, key := range keys {
		currentMap, ok := current.(map[string]any)
		require.True(t, ok, "expected %s to be an object", key)

		current, ok = currentMap[key]
		if !ok {
			return
		}
		if index == len(keys)-1 {
			t.Fatalf("expected %s to be missing", strings.Join(keys, "."))
		}
	}
}

func nestedValue(t *testing.T, root map[string]any, keys ...string) any {
	t.Helper()

	var current any = root
	for _, key := range keys {
		currentMap, ok := current.(map[string]any)
		require.True(t, ok, "expected %s to be an object", key)

		current, ok = currentMap[key]
		require.True(t, ok, "missing %s", key)
	}

	return current
}

func assertSplitDashboardDatasource(t *testing.T, dashboard map[string]any) {
	t.Helper()

	templating, ok := dashboard["templating"].(map[string]any)
	require.True(t, ok)
	vars, ok := templating["list"].([]any)
	require.True(t, ok)

	for _, item := range vars {
		variable, ok := item.(map[string]any)
		require.True(t, ok)
		if variable["name"] != "datasource" {
			continue
		}

		current, ok := variable["current"].(map[string]any)
		require.True(t, ok)
		assert.Equal(t, "neutree-cluster", current["text"])
		assert.Equal(t, "neutree-cluster", current["value"])

		return
	}

	t.Fatalf("datasource variable not found")
}
