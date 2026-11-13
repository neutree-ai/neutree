package observations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/pkg/cacheobservations"
)

const (
	monitoringNoData = "no_data"
	monitoringMatrix = "matrix"
)

type performancePoint struct {
	Timestamp float64 `json:"timestamp"`
	Value     float64 `json:"value"`
}

type performanceSeries struct {
	Metric string             `json:"metric"`
	Unit   string             `json:"unit"`
	Points []performancePoint `json:"points"`
}

type performanceTrends struct {
	Status string              `json:"status"`
	Window string              `json:"window"`
	Start  int64               `json:"start"`
	End    int64               `json:"end"`
	Step   int64               `json:"step_seconds"`
	Series []performanceSeries `json:"series"`
}

func performanceRange(q url.Values, now time.Time) (performanceTrends, error) {
	result := performanceTrends{Window: q.Get("window"), End: now.Unix(), Series: []performanceSeries{}}

	if q.Has("start") || q.Has("end") {
		start, errStart := strconv.ParseInt(q.Get("start"), 10, 64)
		end, errEnd := strconv.ParseInt(q.Get("end"), 10, 64)

		if errStart != nil || errEnd != nil || start < 0 || end <= start || end-start > 30*24*3600 {
			return result, fmt.Errorf("start and end must be Unix seconds spanning at most 30 days")
		}

		result.Window, result.Start, result.End = "custom", start, end
	} else {
		if result.Window == "" {
			result.Window = "5m"
		}

		durations := map[string]int64{"5m": 300, "1h": 3600, "24h": 86400}

		duration, ok := durations[result.Window]
		if !ok {
			return result, fmt.Errorf("window must be 5m, 1h or 24h")
		}

		result.Start = result.End - duration
	}
	// At most 361 points per metric, with a minimum 15-second resolution.
	result.Step = max(int64(15), (result.End-result.Start+359)/360)

	return result, nil
}

func performanceTrendsHandler(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		result, err := performanceRange(c.Request.URL.Query(), time.Now())
		if err != nil {
			c.JSON(400, gin.H{"error": err.Error()})
			return
		}

		workspace, name := c.Param("workspace"), c.Param("name")

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
			result.Status = "not_configured"
			c.JSON(200, result)

			return
		}
		// Each point summarizes a rolling interval, independently of the chart's span.
		interval := strconv.FormatInt(max(int64(60), result.Step), 10) + "s"
		endpoint := items[0]
		queries := []string{buildPerformanceQuery(workspace, endpoint.Spec.Cluster, name, interval)}

		if cache := endpoint.Spec.KVCache; cache != nil {
			provider, _ := deps.KVCacheProviders.Get(cache.Provider)
			if observer, ok := provider.(cacheobservations.PerformanceProvider); ok {
				queries = append(queries, observer.PerformanceQueries(cacheobservations.Scope{
					Workspace: workspace, Cluster: endpoint.Spec.Cluster, Endpoint: name, Window: interval, Nodes: collectEndpointNodes(endpoint.Status),
				})...)
			}
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()

		result.Series, err = queryPerformanceTrends(ctx, deps, strings.Join(queries, " or "), result)
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}

		result.Status = monitoringNoData
		if len(result.Series) > 0 {
			result.Status = "ok"
		}

		c.JSON(200, result)
	}
}

func queryPerformanceTrends(ctx context.Context, deps *Dependencies, query string, bounds performanceTrends) ([]performanceSeries, error) {
	u, err := url.Parse(strings.TrimRight(deps.MetricsURL, "/") + "/api/v1/query_range")
	if err != nil {
		return nil, err
	}

	params := u.Query()
	params.Set("query", query)
	params.Set("start", strconv.FormatInt(bounds.Start, 10))
	params.Set("end", strconv.FormatInt(bounds.End, 10))
	params.Set("step", strconv.FormatInt(bounds.Step, 10))
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

	if resp.StatusCode != http.StatusOK || len(body) > maxResponseBytes {
		return nil, fmt.Errorf("invalid monitoring response")
	}

	return decodePerformanceTrends(body, bounds)
}

func decodePerformanceTrends(body []byte, bounds performanceTrends) ([]performanceSeries, error) {
	var response struct {
		Status    string
		IsPartial bool
		Data      struct {
			ResultType string
			Result     []struct {
				Metric map[string]string
				Values [][2]json.RawMessage
			}
		}
	}

	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}

	if response.Status != monitoringSuccess || response.IsPartial || response.Data.ResultType != monitoringMatrix {
		return nil, fmt.Errorf("invalid monitoring response")
	}

	units := map[string]string{
		"ttft_p95_seconds": "seconds", "ttft_avg_seconds": "seconds", "tpot_p95_seconds": "seconds", "tpot_avg_seconds": "seconds",
		"prompt_tokens_per_second": "tokens_per_second", "generation_tokens_per_second": "tokens_per_second",
		"l0_hit_rate": "ratio", "l1_hit_rate": "ratio", "l1_read_bytes_per_second": "bytes_per_second", "l1_write_bytes_per_second": "bytes_per_second",
	}
	series := []performanceSeries{}
	totals := map[float64]float64{}

	for _, item := range response.Data.Result {
		name := item.Metric["metric"]
		unit, ok := units[name]
		// Cumulative snapshot fallbacks must never fill idle historical intervals.
		if !ok {
			continue
		}

		entry := performanceSeries{Metric: name, Unit: unit, Points: []performancePoint{}}

		for _, pair := range item.Values {
			var timestamp float64

			var raw string
			if json.Unmarshal(pair[0], &timestamp) != nil || json.Unmarshal(pair[1], &raw) != nil {
				continue
			}

			value, err := strconv.ParseFloat(raw, 64)
			if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || timestamp < float64(bounds.Start) || timestamp > float64(bounds.End) {
				continue
			}

			if unit == "ratio" {
				value = math.Min(1, value)
			}

			entry.Points = append(entry.Points, performancePoint{Timestamp: timestamp, Value: value})

			if name == "prompt_tokens_per_second" || name == "generation_tokens_per_second" {
				totals[timestamp] += value
			}
		}

		if len(entry.Points) > 0 {
			series = append(series, entry)
		}
	}

	if len(totals) > 0 {
		total := performanceSeries{Metric: "total_tokens_per_second", Unit: "tokens_per_second", Points: []performancePoint{}}
		for timestamp, value := range totals {
			total.Points = append(total.Points, performancePoint{Timestamp: timestamp, Value: value})
		}

		series = append(series, total)
	}

	for i := range series {
		sort.Slice(series[i].Points, func(a, b int) bool { return series[i].Points[a].Timestamp < series[i].Points[b].Timestamp })
	}

	sort.Slice(series, func(i, j int) bool { return series[i].Metric < series[j].Metric })

	return series, nil
}
