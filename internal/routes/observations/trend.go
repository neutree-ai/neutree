package observations

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
)

// Fixed server-owned bounds keep history queries independent of browser clocks
// and prevent callers from issuing arbitrary PromQL or unbounded range queries.
func trendHandler(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		items, err := deps.Storage.ListCluster(resourceFilter(c.Param("workspace"), c.Param("name")))
		if err != nil {
			c.JSON(500, gin.H{"error": "failed to read cluster"})
			return
		}

		if len(items) != 1 {
			c.JSON(404, gin.H{"error": "cluster not found"})
			return
		}

		c.Header("Cache-Control", "no-store")

		if deps.MetricsURL == "" {
			c.JSON(200, Snapshot{Status: "not_configured", Samples: []Sample{}})
			return
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()

		now := time.Now()

		samples, err := queryTrend(ctx, deps, c.Param("workspace"), c.Param("name"), now)
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}

		status := "ok"
		if len(samples) == 0 {
			status = "no_data"
		}

		end := now.Unix() / 21600 * 21600
		c.JSON(200, gin.H{"status": status, "samples": samples, "start": end - 30*24*3600, "end": end})
	}
}
func queryTrend(ctx context.Context, deps *Dependencies, workspace, cluster string, now time.Time) ([]Sample, error) {
	u, err := url.Parse(strings.TrimRight(deps.MetricsURL, "/") + "/api/v1/query_range")
	if err != nil {
		return nil, err
	}

	end := now.Unix() / 21600 * 21600
	start := end - 30*24*3600
	p := u.Query()
	p.Set("query", fmt.Sprintf("max by (node, accelerator_uuid, product) ("+
		"avg_over_time(neutree_accelerator_utilization_ratio{workspace=%s,neutree_cluster=%s}[6h]))",
		strconv.Quote(workspace), strconv.Quote(cluster)))
	p.Set("start", strconv.FormatInt(start, 10))
	p.Set("end", strconv.FormatInt(end, 10))
	p.Set("step", "21600")
	u.RawQuery = p.Encode()

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
	var result struct {
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

	if err = json.Unmarshal(body, &result); err != nil {
		return nil, err
	}

	if result.Status != "success" || result.IsPartial || result.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("invalid monitoring response")
	}

	samples := []Sample{}

	for _, series := range result.Data.Result {
		if series.Metric["node"] == "" || series.Metric["accelerator_uuid"] == "" {
			continue
		}

		for _, pair := range series.Values {
			var timestamp float64

			var raw string
			if json.Unmarshal(pair[0], &timestamp) != nil || json.Unmarshal(pair[1], &raw) != nil {
				continue
			}

			value, e := strconv.ParseFloat(raw, 64)
			if e != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || value > 1 || timestamp < float64(start) || timestamp > float64(end) {
				continue
			}

			samples = append(samples, Sample{Metric: "neutree_accelerator_utilization_ratio",
				Labels: map[string]string{"node": series.Metric["node"],
					"accelerator_uuid": series.Metric["accelerator_uuid"], "product": series.Metric["product"]},
				Value: value, ObservedAt: timestamp})
		}
	}

	return samples, nil
}
