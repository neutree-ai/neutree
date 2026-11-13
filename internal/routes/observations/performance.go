package observations

import (
	"fmt"
	"strconv"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func collectEndpointNodes(status *v1.EndpointStatus) []string {
	if status == nil || status.Resources == nil {
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

func buildPerformanceQuery(workspace, cluster, endpoint, window string) string {
	sel := fmt.Sprintf("workspace=%s,neutree_cluster=%s,application=%s",
		strconv.Quote(workspace), strconv.Quote(cluster), strconv.Quote(endpoint))

	ttftCountWin := fmt.Sprintf(
		"(sum(increase(vllm:time_to_first_token_seconds_count{%s}[%s])) or "+
			"sum(increase(sglang:time_to_first_token_seconds_count{%s}[%s]))) > 0",
		sel, window, sel, window)
	tpotCountWin := fmt.Sprintf(
		"(sum(increase(vllm:request_time_per_output_token_seconds_count{%s}[%s]) or "+
			"increase(vllm:time_per_output_token_seconds_count{%s}[%s]) or "+
			"increase(sglang:time_per_output_token_seconds_count{%s}[%s]))) > 0",
		sel, window, sel, window, sel, window)
	srcKinds := "local_cache_hit|external_kv_transfer|local_compute"
	srcTotalWin := fmt.Sprintf("sum(increase(vllm:prompt_tokens_by_source_total{%s,source=~%q}[%s])) > 0",
		sel, srcKinds, window)
	l0BySourceWin := fmt.Sprintf(
		"((sum(increase(vllm:prompt_tokens_by_source_total{%s,source=\"local_cache_hit\"}[%s])) or (0 * (%s))) / (%s))",
		sel, window, srcTotalWin, srcTotalWin)
	l0PrefixWin := fmt.Sprintf(
		"(sum(increase(vllm:prefix_cache_hits_total{%s}[%s]) or increase(vllm:gpu_prefix_cache_hits_total{%s}[%s])) / "+
			"((sum(increase(vllm:prefix_cache_queries_total{%s}[%s]) or "+
			"increase(vllm:gpu_prefix_cache_queries_total{%s}[%s]))) > 0))",
		sel, window, sel, window, sel, window, sel, window)

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
		labeledMetric("prompt_tokens_per_second", fmt.Sprintf(
			"sum(rate(vllm:prompt_tokens_total{%s}[%s])) or sum(rate(sglang:prompt_tokens_total{%s}[%s]))",
			sel, window, sel, window)),
		labeledMetric("generation_tokens_per_second", fmt.Sprintf(
			"sum(rate(vllm:generation_tokens_total{%s}[%s])) or "+
				"sum(rate(sglang:generation_tokens_total{%s}[%s])) or sum(sglang:gen_throughput{%s})",
			sel, window, sel, window, sel)),
		labeledMetric("l0_hit_rate", fmt.Sprintf("%s or %s", l0BySourceWin, l0PrefixWin)),
	}

	return strings.Join(parts, " or ")
}
