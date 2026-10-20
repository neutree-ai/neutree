// Package observations serves bounded, resource-scoped monitoring snapshots.
// Measurements deliberately stay out of the resource reconciliation/write path.
package observations

import (
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

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const maxResponseBytes = 8 << 20

var physicalMetrics = []string{
	"neutree_accelerator_memory_used_bytes", "neutree_accelerator_memory_total_bytes",
	"neutree_accelerator_utilization_ratio", "neutree_accelerator_temperature_celsius",
	"neutree_accelerator_power_usage_watts", "neutree_node_accelerator_hardware_info",
	"neutree_node_accelerator_nvidia_info", "neutree_node_accelerator_ascend_info",
	"neutree_node_accelerator_hygon_info",
}

var replicaMetrics = []string{
	"neutree_endpoint_replica_accelerator_memory_used_bytes",
	"neutree_endpoint_replica_accelerator_utilization_ratio",
}

type Dependencies struct {
	Storage    storage.Storage
	MetricsURL string
	Client     *http.Client
}

type Sample struct {
	Metric     string            `json:"metric"`
	Labels     map[string]string `json:"labels"`
	Value      float64           `json:"value"`
	ObservedAt float64           `json:"observed_at"`
}

type Snapshot struct {
	Status  string   `json:"status"`
	Samples []Sample `json:"samples"`
}

func RegisterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	if deps.Client == nil {
		deps.Client = &http.Client{Timeout: 5 * time.Second}
	}
	for _, resource := range []string{"clusters", "endpoints"} {
		route := group.Group("/" + resource + "/:workspace/:name")
		route.Use(middlewares...)
		permission := "cluster:read"
		if resource == "endpoints" {
			permission = "endpoint:read"
		}
		route.GET("/accelerator-observations", middleware.RequireWorkspacePermission(permission,
			middleware.PermissionDependencies{Storage: deps.Storage}), handler(deps, resource))
	}
}

func resourceFilter(workspace, name string) storage.ListOption {
	return storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(name)},
	}}
}

func handler(deps *Dependencies, resource string) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace, name := c.Param("workspace"), c.Param("name")
		cluster, endpoint := name, ""
		metrics := append([]string{}, physicalMetrics...)
		if resource == "endpoints" {
			items, err := deps.Storage.ListEndpoint(resourceFilter(workspace, name))
			if err != nil {
				c.JSON(500, gin.H{"error": "failed to read endpoint"})
				return
			}
			if len(items) != 1 || items[0].Spec == nil {
				c.JSON(404, gin.H{"error": "endpoint not found"})
				return
			}
			cluster, endpoint = items[0].Spec.Cluster, name
			metrics = replicaMetrics
		} else {
			items, err := deps.Storage.ListCluster(resourceFilter(workspace, name))
			if err != nil {
				c.JSON(500, gin.H{"error": "failed to read cluster"})
				return
			}
			if len(items) != 1 {
				c.JSON(404, gin.H{"error": "cluster not found"})
				return
			}
			// Cluster visibility alone must not reveal workload measurements.
			allowed, err := middleware.CheckWorkspacePermission(deps.Storage, c.GetString("user_id"), workspace, "endpoint:read")
			if err != nil {
				c.JSON(500, gin.H{"error": "failed to check permissions"})
				return
			}
			if allowed {
				metrics = append(metrics, replicaMetrics...)
			}
		}
		c.Header("Cache-Control", "no-store")
		if deps.MetricsURL == "" {
			c.JSON(200, Snapshot{Status: "not_configured", Samples: []Sample{}})
			return
		}
		selector := fmt.Sprintf("{__name__=~%s,workspace=%s,neutree_cluster=%s", strconv.Quote(strings.Join(metrics, "|")), strconv.Quote(workspace), strconv.Quote(cluster))
		if endpoint != "" {
			selector += ",endpoint=" + strconv.Quote(endpoint)
		}
		// A range vector preserves original scrape timestamps. An instant vector
		// reports evaluation time and can make old measurements look fresh.
		query := selector + "}[90s]"
		u, err := url.Parse(strings.TrimRight(deps.MetricsURL, "/") + "/api/v1/query")
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}
		params := u.Query()
		params.Set("query", query)
		u.RawQuery = params.Encode()
		req, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, u.String(), nil)
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}
		resp, err := deps.Client.Do(req)
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}
		defer resp.Body.Close()
		body, err := io.ReadAll(io.LimitReader(resp.Body, maxResponseBytes+1))
		if err != nil || resp.StatusCode != 200 || len(body) > maxResponseBytes {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}
		samples, err := decodeSamples(body, time.Now())
		if err != nil {
			c.JSON(503, gin.H{"error": "monitoring unavailable"})
			return
		}
		status := "ok"
		if len(samples) == 0 {
			status = "no_data"
		}
		c.JSON(200, Snapshot{Status: status, Samples: samples})
	}
}

func decodeSamples(body []byte, now time.Time) ([]Sample, error) {
	var response struct {
		Status    string `json:"status"`
		IsPartial bool   `json:"isPartial"`
		Data      struct {
			ResultType string `json:"resultType"`
			Result     []struct {
				Metric map[string]string    `json:"metric"`
				Values [][2]json.RawMessage `json:"values"`
			} `json:"result"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return nil, err
	}
	if response.Status != "success" || response.IsPartial || response.Data.ResultType != "matrix" {
		return nil, fmt.Errorf("invalid monitoring response")
	}
	result := []Sample{}
	for _, series := range response.Data.Result {
		if len(series.Values) == 0 || series.Metric["accelerator_uuid"] == "" {
			continue
		}
		last := series.Values[len(series.Values)-1]
		var timestamp float64
		var raw string
		if json.Unmarshal(last[0], &timestamp) != nil || json.Unmarshal(last[1], &raw) != nil {
			continue
		}
		value, err := strconv.ParseFloat(raw, 64)
		if err != nil || math.IsNaN(value) || math.IsInf(value, 0) || value < 0 || timestamp < float64(now.Unix()-90) || timestamp > float64(now.Unix()+5) {
			continue
		}
		name := series.Metric["__name__"]
		labels := map[string]string{}
		for _, key := range strings.Fields("node accelerator_uuid accelerator_index accelerator_type product endpoint replica instance_id vdevice_index memory_total_bytes architecture cuda_capability driver_version cuda_driver_version pcie_bus_id pcie_generation pcie_width numa_node nvlink nvswitch") {
			if value, ok := series.Metric[key]; ok {
				labels[key] = value
			}
		}
		result = append(result, Sample{Metric: name, Labels: labels, Value: value, ObservedAt: timestamp})
	}
	return result, nil
}
