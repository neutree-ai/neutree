package observations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type PerformanceMetrics struct {
	TTFTP95Seconds            *float64 `json:"ttft_p95_seconds,omitempty"`
	TTFTAvgSeconds            *float64 `json:"ttft_avg_seconds,omitempty"`
	TPOTP95Seconds            *float64 `json:"tpot_p95_seconds,omitempty"`
	TPOTAvgSeconds            *float64 `json:"tpot_avg_seconds,omitempty"`
	LatencyCumulative         bool     `json:"latency_cumulative,omitempty"`
	PromptTokensPerSecond     *float64 `json:"prompt_tokens_per_second,omitempty"`
	GenerationTokensPerSecond *float64 `json:"generation_tokens_per_second,omitempty"`
	TotalTokensPerSecond      *float64 `json:"total_tokens_per_second,omitempty"`
	RequestsPerSecond         *float64 `json:"requests_per_second,omitempty"`
	L0HitRate                 *float64 `json:"l0_hit_rate,omitempty"`
	L1HitRate                 *float64 `json:"l1_hit_rate,omitempty"`
	HitRateCumulative         bool     `json:"hit_rate_cumulative,omitempty"`
	L1ReadBytesPerSecond      *float64 `json:"l1_read_bytes_per_second,omitempty"`
	L1WriteBytesPerSecond     *float64 `json:"l1_write_bytes_per_second,omitempty"`
}

type PerformanceSnapshot struct {
	Status  string             `json:"status"`
	Window  string             `json:"window"`
	Metrics PerformanceMetrics `json:"metrics"`
}

func normalizePerformanceWindow(raw string) string {
	switch raw {
	case "1h", "24h":
		return raw
	default:
		return "5m"
	}
}

func performanceHandler(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace, name := c.Param("workspace"), c.Param("name")
		window := normalizePerformanceWindow(c.Query("window"))

		items, err := deps.Storage.ListEndpoint(resourceFilter(workspace, name))
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to read endpoint"})
			return
		}

		if len(items) != 1 || items[0].Spec == nil {
			c.JSON(404, gin.H{"error": "endpoint not found"})
			return
		}

		c.Header("Cache-Control", "no-store")

		if deps.MetricsURL == "" {
			c.JSON(200, PerformanceSnapshot{Status: "not_configured", Window: window})
			return
		}

		status := items[0].Status
		if status != nil && (status.Phase == "Paused" || status.Phase == "Deleted") {
			c.JSON(200, PerformanceSnapshot{Status: "no_data", Window: window})
			return
		}

		zcacheEnabled := items[0].Spec.KVCache != nil && items[0].Spec.KVCache.Provider == "zcache"
		nodes := collectEndpointNodes(zcacheEnabled, status)

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()

		query := buildPerformanceQuery(workspace, items[0].Spec.Cluster, name, window, zcacheEnabled, nodes)

		values, err := queryVector(ctx, deps, query)
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}

		if len(values) == 0 {
			c.JSON(200, PerformanceSnapshot{Status: "no_data", Window: window})
			return
		}

		c.JSON(200, PerformanceSnapshot{
			Status:  "ok",
			Window:  window,
			Metrics: assemblePerformanceMetrics(values),
		})
	}
}

func collectEndpointNodes(zcacheEnabled bool, status *v1.EndpointStatus) []string {
	if !zcacheEnabled || status == nil || status.Resources == nil {
		return nil
	}

	var nodes []string

	seen := map[string]bool{}

	for _, replica := range status.Resources.Replicas {
		if replica.NodeID != "" && !seen[replica.NodeID] {
			nodes = append(nodes, replica.NodeID)
			seen[replica.NodeID] = true
		}

		for _, device := range replica.Devices {
			if device.NodeID != "" && !seen[device.NodeID] {
				nodes = append(nodes, device.NodeID)
				seen[device.NodeID] = true
			}
		}
	}

	return nodes
}

func labeledMetric(name, expr string) string {
	return fmt.Sprintf(`label_replace((%s), "metric", %s, "", "")`, expr, strconv.Quote(name))
}

func buildPerformanceQuery(workspace, cluster, endpoint, window string, zcacheEnabled bool, nodes []string) string {
	sel := fmt.Sprintf("workspace=%s,neutree_cluster=%s,application=%s",
		strconv.Quote(workspace), strconv.Quote(cluster), strconv.Quote(endpoint))

	ttftCountWin := fmt.Sprintf(
		"(sum(increase(vllm:time_to_first_token_seconds_count{%s}[%s])) or "+
			"sum(increase(sglang:time_to_first_token_seconds_count{%s}[%s]))) > 0",
		sel, window, sel, window)
	ttftCountCum := fmt.Sprintf(
		"(sum(vllm:time_to_first_token_seconds_count{%s}) or "+
			"sum(sglang:time_to_first_token_seconds_count{%s})) > 0",
		sel, sel)

	tpotCountWin := fmt.Sprintf(
		"(sum(increase(vllm:request_time_per_output_token_seconds_count{%s}[%s]) or "+
			"increase(vllm:time_per_output_token_seconds_count{%s}[%s]) or "+
			"increase(sglang:time_per_output_token_seconds_count{%s}[%s]))) > 0",
		sel, window, sel, window, sel, window)
	tpotCountCum := fmt.Sprintf(
		"(sum(vllm:request_time_per_output_token_seconds_count{%s} or "+
			"vllm:time_per_output_token_seconds_count{%s} or "+
			"sglang:time_per_output_token_seconds_count{%s})) > 0",
		sel, sel, sel)

	srcKinds := "local_cache_hit|external_kv_transfer|local_compute"
	srcTotalWin := fmt.Sprintf("sum(increase(vllm:prompt_tokens_by_source_total{%s,source=~%q}[%s])) > 0",
		sel, srcKinds, window)
	srcTotalCum := fmt.Sprintf("sum(vllm:prompt_tokens_by_source_total{%s,source=~%q}) > 0",
		sel, srcKinds)

	l0BySourceWin := fmt.Sprintf(
		"((sum(increase(vllm:prompt_tokens_by_source_total{%s,source=\"local_cache_hit\"}[%s])) or (0 * (%s))) / (%s))",
		sel, window, srcTotalWin, srcTotalWin)
	l0PrefixWin := fmt.Sprintf(
		"(sum(increase(vllm:prefix_cache_hits_total{%s}[%s]) or increase(vllm:gpu_prefix_cache_hits_total{%s}[%s])) / "+
			"((sum(increase(vllm:prefix_cache_queries_total{%s}[%s]) or "+
			"increase(vllm:gpu_prefix_cache_queries_total{%s}[%s]))) > 0))",
		sel, window, sel, window, sel, window, sel, window)

	l0BySourceCum := fmt.Sprintf(
		"((sum(vllm:prompt_tokens_by_source_total{%s,source=\"local_cache_hit\"}) or (0 * (%s))) / (%s))",
		sel, srcTotalCum, srcTotalCum)
	l0PrefixCum := fmt.Sprintf(
		"(sum(vllm:prefix_cache_hits_total{%s} or vllm:gpu_prefix_cache_hits_total{%s}) / "+
			"((sum(vllm:prefix_cache_queries_total{%s} or vllm:gpu_prefix_cache_queries_total{%s})) > 0))",
		sel, sel, sel, sel)

	l1WinExpr := fmt.Sprintf(
		"((sum(increase(vllm:prompt_tokens_by_source_total{%s,source=\"external_kv_transfer\"}[%s])) or (0 * (%s))) / (%s))",
		sel, window, srcTotalWin, srcTotalWin)
	l1CumExpr := fmt.Sprintf(
		"((sum(vllm:prompt_tokens_by_source_total{%s,source=\"external_kv_transfer\"}) or (0 * (%s))) / (%s))",
		sel, srcTotalCum, srcTotalCum)

	parts := []string{
		labeledMetric("ttft_p95_seconds", fmt.Sprintf(
			"histogram_quantile(0.95, (sum by (le) (increase(vllm:time_to_first_token_seconds_bucket{%s}[%s])) or "+
				"sum by (le) (increase(sglang:time_to_first_token_seconds_bucket{%s}[%s]))) and on() (%s))",
			sel, window, sel, window, ttftCountWin)),
		labeledMetric("ttft_avg_seconds", fmt.Sprintf(
			"(sum(increase(vllm:time_to_first_token_seconds_sum{%s}[%s])) or "+
				"sum(increase(sglang:time_to_first_token_seconds_sum{%s}[%s]))) / (%s)",
			sel, window, sel, window, ttftCountWin)),
		labeledMetric("tpot_p95_seconds", fmt.Sprintf(
			"histogram_quantile(0.95, (sum by (le) ("+
				"increase(vllm:request_time_per_output_token_seconds_bucket{%s}[%s]) or "+
				"increase(vllm:time_per_output_token_seconds_bucket{%s}[%s]) or "+
				"increase(sglang:time_per_output_token_seconds_bucket{%s}[%s]))) and on() (%s))",
			sel, window, sel, window, sel, window, tpotCountWin)),
		labeledMetric("tpot_avg_seconds", fmt.Sprintf(
			"(sum(increase(vllm:request_time_per_output_token_seconds_sum{%s}[%s]) or "+
				"increase(vllm:time_per_output_token_seconds_sum{%s}[%s]) or "+
				"increase(sglang:time_per_output_token_seconds_sum{%s}[%s]))) / (%s)",
			sel, window, sel, window, sel, window, tpotCountWin)),
		labeledMetric("cum_ttft_p95_seconds", fmt.Sprintf(
			"histogram_quantile(0.95, (sum by (le) (vllm:time_to_first_token_seconds_bucket{%s}) or "+
				"sum by (le) (sglang:time_to_first_token_seconds_bucket{%s})) and on() (%s))",
			sel, sel, ttftCountCum)),
		labeledMetric("cum_ttft_avg_seconds", fmt.Sprintf(
			"(sum(vllm:time_to_first_token_seconds_sum{%s}) or "+
				"sum(sglang:time_to_first_token_seconds_sum{%s})) / (%s)",
			sel, sel, ttftCountCum)),
		labeledMetric("cum_tpot_p95_seconds", fmt.Sprintf(
			"histogram_quantile(0.95, (sum by (le) ("+
				"vllm:request_time_per_output_token_seconds_bucket{%s} or "+
				"vllm:time_per_output_token_seconds_bucket{%s} or "+
				"sglang:time_per_output_token_seconds_bucket{%s})) and on() (%s))",
			sel, sel, sel, tpotCountCum)),
		labeledMetric("cum_tpot_avg_seconds", fmt.Sprintf(
			"(sum(vllm:request_time_per_output_token_seconds_sum{%s} or "+
				"vllm:time_per_output_token_seconds_sum{%s} or "+
				"sglang:time_per_output_token_seconds_sum{%s})) / (%s)",
			sel, sel, sel, tpotCountCum)),
		labeledMetric("prompt_tokens_per_second", fmt.Sprintf(
			"sum(rate(vllm:prompt_tokens_total{%s}[%s])) or sum(rate(sglang:prompt_tokens_total{%s}[%s]))",
			sel, window, sel, window)),
		labeledMetric("generation_tokens_per_second", fmt.Sprintf(
			"sum(rate(vllm:generation_tokens_total{%s}[%s])) or "+
				"sum(rate(sglang:generation_tokens_total{%s}[%s])) or sum(sglang:gen_throughput{%s})",
			sel, window, sel, window, sel)),
		labeledMetric("requests_per_second", fmt.Sprintf(
			"sum(rate(vllm:request_success_total{%s}[%s])) or sum(rate(sglang:num_requests_total{%s}[%s]))",
			sel, window, sel, window)),
		labeledMetric("l0_hit_rate", fmt.Sprintf("%s or %s", l0BySourceWin, l0PrefixWin)),
		labeledMetric("cum_l0_hit_rate", fmt.Sprintf("%s or %s", l0BySourceCum, l0PrefixCum)),
	}

	if zcacheEnabled && len(nodes) > 0 {
		escaped := make([]string, 0, len(nodes))
		for _, node := range nodes {
			escaped = append(escaped, regexp.QuoteMeta(node))
		}

		nodeRegex := "^(" + strings.Join(escaped, "|") + ")$"
		l1Sel := fmt.Sprintf(`job="neutree-zcache",workspace=%s,neutree_cluster=%s,node=~%s`,
			strconv.Quote(workspace), strconv.Quote(cluster), strconv.Quote(nodeRegex))

		l1ZcacheWin := fmt.Sprintf(
			"(sum(sum by(node) (increase(lmcache_mp_lookup_hit_tokens_total{%s}[%s]) and on(job,instance) (up{%s} == 1))) / "+
				"((sum(sum by(node) (increase(lmcache_mp_lookup_requested_tokens_total{%s}[%s]) and "+
				"on(job,instance) (up{%s} == 1)))) > 0))",
			l1Sel, window, l1Sel, l1Sel, window, l1Sel)
		l1ZcacheCum := fmt.Sprintf(
			"(sum(max by(node) (lmcache_mp_lookup_hit_tokens_total{%s} and on(job,instance) (up{%s} == 1))) / "+
				"((sum(max by(node) (lmcache_mp_lookup_requested_tokens_total{%s} and "+
				"on(job,instance) (up{%s} == 1)))) > 0))",
			l1Sel, l1Sel, l1Sel, l1Sel)

		l1WinExpr = fmt.Sprintf("%s or %s", l1WinExpr, l1ZcacheWin)
		l1CumExpr = fmt.Sprintf("%s or %s", l1CumExpr, l1ZcacheCum)

		parts = append(parts,
			labeledMetric("l1_read_bytes_per_second", fmt.Sprintf(
				`sum(sum by(node) (rate(lmcache_mp_transfer_phase_bytes_total{%s,phase="staging",direction="h2d"}[%s]) `+
					`and on(job,instance) (up{%s} == 1))) or `+
					`sum(sum by(node) (rate(lmcache_mp_read_bytes_total{%s}[%s]) and on(job,instance) (up{%s} == 1))) or `+
					`(0 * (sum(up{%s} == 1) > 0))`,
				l1Sel, window, l1Sel, l1Sel, window, l1Sel, l1Sel)),
			labeledMetric("l1_write_bytes_per_second", fmt.Sprintf(
				`sum(sum by(node) (rate(lmcache_mp_transfer_phase_bytes_total{%s,phase="staging",direction="d2h"}[%s]) `+
					`and on(job,instance) (up{%s} == 1))) or `+
					`sum(sum by(node) (rate(lmcache_mp_write_bytes_total{%s}[%s]) and on(job,instance) (up{%s} == 1))) or `+
					`(0 * (sum(up{%s} == 1) > 0))`,
				l1Sel, window, l1Sel, l1Sel, window, l1Sel, l1Sel)),
		)
	}

	parts = append(parts,
		labeledMetric("l1_hit_rate", l1WinExpr),
		labeledMetric("cum_l1_hit_rate", l1CumExpr),
	)

	return strings.Join(parts, " or ")
}

func assemblePerformanceMetrics(values map[string]float64) PerformanceMetrics {
	pick := func(key string) *float64 {
		v, ok := values[key]
		if !ok {
			return nil
		}

		return &v
	}

	clampRatio := func(v *float64) *float64 {
		if v == nil {
			return nil
		}

		clamped := math.Max(0, math.Min(1, *v))

		return &clamped
	}

	m := PerformanceMetrics{
		TTFTP95Seconds:            pick("ttft_p95_seconds"),
		TTFTAvgSeconds:            pick("ttft_avg_seconds"),
		TPOTP95Seconds:            pick("tpot_p95_seconds"),
		TPOTAvgSeconds:            pick("tpot_avg_seconds"),
		PromptTokensPerSecond:     pick("prompt_tokens_per_second"),
		GenerationTokensPerSecond: pick("generation_tokens_per_second"),
		RequestsPerSecond:         pick("requests_per_second"),
		L0HitRate:                 clampRatio(pick("l0_hit_rate")),
		L1HitRate:                 clampRatio(pick("l1_hit_rate")),
		L1ReadBytesPerSecond:      pick("l1_read_bytes_per_second"),
		L1WriteBytesPerSecond:     pick("l1_write_bytes_per_second"),
	}

	if m.TTFTP95Seconds == nil && m.TTFTAvgSeconds == nil && m.TPOTP95Seconds == nil && m.TPOTAvgSeconds == nil {
		m.TTFTP95Seconds = pick("cum_ttft_p95_seconds")
		m.TTFTAvgSeconds = pick("cum_ttft_avg_seconds")
		m.TPOTP95Seconds = pick("cum_tpot_p95_seconds")
		m.TPOTAvgSeconds = pick("cum_tpot_avg_seconds")
		m.LatencyCumulative = m.TTFTP95Seconds != nil || m.TTFTAvgSeconds != nil || m.TPOTP95Seconds != nil || m.TPOTAvgSeconds != nil
	}

	if m.L0HitRate == nil && m.L1HitRate == nil {
		m.L0HitRate = clampRatio(pick("cum_l0_hit_rate"))
		m.L1HitRate = clampRatio(pick("cum_l1_hit_rate"))
		m.HitRateCumulative = m.L0HitRate != nil || m.L1HitRate != nil
	}

	if m.PromptTokensPerSecond != nil || m.GenerationTokensPerSecond != nil {
		total := 0.0
		if m.PromptTokensPerSecond != nil {
			total += *m.PromptTokensPerSecond
		}

		if m.GenerationTokensPerSecond != nil {
			total += *m.GenerationTokensPerSecond
		}

		m.TotalTokensPerSecond = &total
	}

	return m
}

func queryVector(ctx context.Context, deps *Dependencies, query string) (map[string]float64, error) {
	u, err := url.Parse(strings.TrimRight(deps.MetricsURL, "/") + "/api/v1/query")
	if err != nil {
		return nil, err
	}

	params := u.Query()
	params.Set("query", query)
	u.RawQuery = params.Encode()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if err != nil {
		return nil, err
	}

	resp, err := deps.Client.Do(req)
	if err != nil {
		return nil, err
	}

	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != 200 || len(body) > maxResponseBytes {
		return nil, fmt.Errorf("invalid monitoring response")
	}

	return decodeVector(body)
}

func decodeVector(body []byte) (map[string]float64, error) {
	var response struct {
		Status    string `json:"status"`
		IsPartial bool   `json:"isPartial"`
		Data      struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string  `json:"metric"`
				Value  [2]json.RawMessage `json:"value"`
			} `json:"result"`
		} `json:"data"`
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	if response.Status != "success" || response.IsPartial || response.Data.ResultType != "vector" {
		return nil, fmt.Errorf("invalid monitoring response")
	}

	values := map[string]float64{}

	for _, series := range response.Data.Result {
		name := series.Metric["metric"]
		if name == "" {
			continue
		}

		var raw string
		if json.Unmarshal(series.Value[1], &raw) != nil {
			continue
		}

		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 {
			continue
		}

		values[name] = value
	}

	return values, nil
}
