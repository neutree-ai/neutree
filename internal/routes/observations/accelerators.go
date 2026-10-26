// Package observations serves bounded, resource-scoped monitoring snapshots.
// Measurements deliberately stay out of the resource reconciliation/write path.
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

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const maxResponseBytes = 8 << 20

var physicalMetrics = []string{
	"neutree_accelerator_memory_used_bytes", "neutree_accelerator_memory_total_bytes",
	"neutree_accelerator_utilization_ratio", "neutree_accelerator_temperature_celsius",
	"neutree_accelerator_power_usage_watts", "neutree_node_accelerator_hardware_info",
	"neutree_node_accelerator_nvidia_info", "neutree_node_accelerator_npu_info",
	"neutree_node_accelerator_hcu_info", "neutree_node_accelerator_iluvatar_info",
}

var replicaMetrics = []string{
	"neutree_endpoint_replica_accelerator_memory_used_bytes",
	"neutree_endpoint_replica_accelerator_utilization_ratio",
	"vllm:kv_cache_usage_perc",
	"vllm:gpu_cache_usage_perc",
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

		if resource == "clusters" {
			route.GET("/accelerator-utilization-trend", middleware.RequireWorkspacePermission(permission,
				middleware.PermissionDependencies{Storage: deps.Storage}), trendHandler(deps))
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
		var devices [][2]string
		zcacheEnabled := false

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
			zcacheEnabled = items[0].Spec.KVCache != nil && items[0].Spec.KVCache.Provider == "zcache"
			metrics = replicaMetrics

			if status := items[0].Status; status != nil && status.Resources != nil && status.Phase != "Paused" && status.Phase != "Deleted" {
				seen := map[[2]string]bool{}

				for _, replica := range status.Resources.Replicas {
					for _, device := range replica.Devices {
						node := device.NodeID
						if node == "" {
							node = replica.NodeID
						}

						key := [2]string{node, device.UUID}
						if node != "" && device.UUID != "" && !seen[key] {
							devices = append(devices, key)
							seen[key] = true
						}
					}
				}
			}
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
		queries := []string{selector + "}[90s]"}
		// Read usage and scrape health together as range vectors, preserving the
		// actual sample timestamps rather than replacing them with query time.
		l1Selector := fmt.Sprintf(`{__name__=~"lmcache_mp_l1_memory_usage_bytes|up",job="neutree-zcache",workspace=%s,neutree_cluster=%s`,
			strconv.Quote(workspace), strconv.Quote(cluster))
		if resource == "clusters" {
			queries = append(queries, l1Selector+"}[90s]")
		}

		if len(devices) > 0 {
			physical := []string{"neutree_accelerator_memory_total_bytes", "neutree_accelerator_temperature_celsius", "neutree_accelerator_power_usage_watts", "neutree_node_accelerator_hardware_info", "neutree_node_accelerator_nvidia_info", "neutree_node_accelerator_npu_info", "neutree_node_accelerator_hcu_info", "neutree_node_accelerator_iluvatar_info"}

			byNode := map[string][]string{}
			for _, device := range devices {
				byNode[device[0]] = append(byNode[device[0]], regexp.QuoteMeta(device[1]))
			}

			for node, uuids := range byNode {
				queries = append(queries, fmt.Sprintf("{__name__=~%s,workspace=%s,neutree_cluster=%s,node=%s,accelerator_uuid=~%s}[90s]", strconv.Quote(strings.Join(physical, "|")), strconv.Quote(workspace), strconv.Quote(cluster), strconv.Quote(node), strconv.Quote(strings.Join(uuids, "|"))))
				if zcacheEnabled {
					queries = append(queries, l1Selector+",node="+strconv.Quote(node)+"}[90s]")
				}
			}
		}

		if zcacheEnabled {
			queries = append(queries, fmt.Sprintf("{__name__=~\"vllm:kv_cache_usage_perc|vllm:gpu_cache_usage_perc\",workspace=%s,neutree_cluster=%s,application=%s}[90s]", strconv.Quote(workspace), strconv.Quote(cluster), strconv.Quote(endpoint)))
		}

		ctx, cancel := context.WithTimeout(c.Request.Context(), 5*time.Second)
		defer cancel()

		samples := []Sample{}

		for _, query := range queries {
			batch, err := querySamples(ctx, deps, query)
			if err != nil {
				c.JSON(503, gin.H{"error": "monitoring unavailable"})
				return
			}

			samples = append(samples, batch...)
		}

		status := "ok"

		if len(samples) == 0 {
			status = "no_data"
		}

		c.JSON(200, Snapshot{Status: status, Samples: samples})
	}
}

func querySamples(ctx context.Context, deps *Dependencies, query string) ([]Sample, error) {
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

	return decodeSamples(body, time.Now())
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
	up := map[[2]string]bool{}

	for _, series := range response.Data.Result {
		if series.Metric["__name__"] != "up" || series.Metric["job"] != "neutree-zcache" || len(series.Values) == 0 {
			continue
		}

		last := series.Values[len(series.Values)-1]
		var timestamp float64

		var raw string
		if json.Unmarshal(last[0], &timestamp) == nil && json.Unmarshal(last[1], &raw) == nil &&
			timestamp >= float64(now.Unix()-90) && timestamp <= float64(now.Unix()+5) {
			up[[2]string{series.Metric["job"], series.Metric["instance"]}] = raw == "1"
		}
	}

	for _, series := range response.Data.Result {
		if len(series.Values) == 0 {
			continue
		}

		name := series.Metric["__name__"]
		isKVCacheUsage := name == "vllm:kv_cache_usage_perc" || name == "vllm:gpu_cache_usage_perc"
		isNodeL1Usage := name == "lmcache_mp_l1_memory_usage_bytes"

		if isKVCacheUsage {
			if series.Metric["replica"] == "" || (series.Metric["endpoint"] == "" && series.Metric["application"] == "") {
				continue
			}

			name = "vllm:kv_cache_usage_perc"
		} else if isNodeL1Usage {
			if series.Metric["node"] == "" || !up[[2]string{series.Metric["job"], series.Metric["instance"]}] {
				continue
			}
		} else if series.Metric["accelerator_uuid"] == "" {
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

		labels := map[string]string{}

		for _, key := range strings.Fields("node accelerator_uuid accelerator_index accelerator_type product endpoint replica instance_id vdevice_index " +
			"memory_total_bytes architecture cuda_capability driver_version cuda_driver_version pcie_bus_id pcie_generation pcie_width numa_node nvlink nvswitch hccs_capable") {
			if value, ok := series.Metric[key]; ok {
				labels[key] = value
			}
		}

		if labels["endpoint"] == "" && series.Metric["application"] != "" {
			labels["endpoint"] = series.Metric["application"]
		}

		if isNodeL1Usage && series.Metric["pod"] != "" {
			labels["pod"] = series.Metric["pod"]
		}

		result = append(result, Sample{Metric: name, Labels: labels, Value: value, ObservedAt: timestamp})
	}

	return result, nil
}
