package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/ray/dashboard"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage    storage.Storage
	HTTPClient util.HTTPClient
	K8sClient  util.K8sClient
}

// LogSourcesResponse represents the response for log sources discovery
type LogSourcesResponse struct {
	Deployments []DeploymentInfo `json:"deployments"`
}

// DeploymentInfo represents deployment information
type DeploymentInfo struct {
	Name     string        `json:"name"`
	Replicas []ReplicaInfo `json:"replicas"`
	Actors   []ActorInfo   `json:"actors,omitempty"`
}

// ReplicaInfo represents replica information.
//
// Failed marks a synthesized replica recovered from the Ray state API
// after Ray Serve removed the actor from the live applications response;
// only stderr / stdout log types are available in that case (no
// log_file_path means the application log file cannot be addressed).
type ReplicaInfo struct {
	ReplicaID string    `json:"replica_id"`
	Failed    bool      `json:"failed,omitempty"`
	Logs      []LogInfo `json:"logs"`
}

// ActorInfo represents a non-Serve actor that belongs to a Serve replica.
type ActorInfo struct {
	ReplicaID string    `json:"replica_id"`
	ActorID   string    `json:"actor_id"`
	ActorName string    `json:"actor_name"`
	Role      string    `json:"role,omitempty"`
	Rank      *int      `json:"rank,omitempty"`
	Logs      []LogInfo `json:"logs"`
}

// LogInfo represents log information
type LogInfo struct {
	Type          string `json:"type"` // "application" | "stderr" | "stdout" | "logs"
	URL           string `json:"url"`
	DownloadURL   string `json:"download_url"`
	ContainerName string `json:"container_name,omitempty"`
	Role          string `json:"role,omitempty"`
	Rank          *int   `json:"rank,omitempty"`
	ActorName     string `json:"actor_name,omitempty"`
	ActorID       string `json:"actor_id,omitempty"`
}

const (
	logTypeApplication = "application"
	logTypeStderr      = "stderr"
	logTypeStdout      = "stdout"
)

// RayApplicationsResponse represents Ray Dashboard API response structure.
//
// TODO: Ray dashboard API types and calls belong in `internal/ray/dashboard/`,
// not in the routes layer. These types (RayApplicationsResponse,
// RayApplication, RayDeployment, RayReplica) and the inline
// `httpClient.Get("/api/serve/applications/")` calls in getRayLogSources
// and streamRayLogs should be replaced with a typed
// `dashboard.GetServeApplicationsForLogs` (or equivalent) so callers go
// through `dashboard.Client.doRequest` instead of hand-rolled HTTP.
type RayApplicationsResponse struct {
	Applications map[string]RayApplication `json:"applications"`
}

// RayApplication represents a Ray application
type RayApplication struct {
	Name        string                   `json:"name"`
	Deployments map[string]RayDeployment `json:"deployments"`
}

// RayDeployment represents a Ray deployment
type RayDeployment struct {
	Replicas []RayReplica `json:"replicas"`
}

// RayReplica represents a Ray replica
type RayReplica struct {
	NodeID      string `json:"node_id"`
	ActorID     string `json:"actor_id"`
	LogFilePath string `json:"log_file_path"`
	ReplicaID   string `json:"replica_id"`
}

// setupStreamingResponse configures response headers for streaming logs
func setupStreamingResponse(c *gin.Context, download bool, filename string) {
	// Set chunked transfer encoding for consistent streaming behavior
	c.Header("Content-Type", "text/plain; charset=utf-8")
	c.Header("Transfer-Encoding", "chunked")
	c.Header("X-Content-Type-Options", "nosniff")

	if download {
		c.Header("Content-Disposition", fmt.Sprintf("attachment; filename=%s", filename))
	}

	c.Status(http.StatusOK)
}

// RegisterEndpointLogsRoutes registers endpoint logs routes
func RegisterEndpointLogsRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	logsGroup := group.Group("/endpoints/:workspace/:name")
	logsGroup.Use(middlewares...)

	// Log sources discovery
	logsGroup.GET("/log-sources", handleGetLogSources(deps))

	// Log content retrieval - support both GET and HEAD for download compatibility
	logHandler := handleGetLogs(deps)
	logsGroup.GET("/logs/:replica_id/:log_type", logHandler)
	logsGroup.HEAD("/logs/:replica_id/:log_type", logHandler)
}

// handleGetLogSources handles the log sources discovery request
func handleGetLogSources(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Param("workspace")
		name := c.Param("name")

		if workspace == "" || name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "workspace and name are required",
			})

			return
		}

		// Get endpoint
		endpoint, cluster, err := getEndpointAndCluster(deps, workspace, name)
		if err != nil {
			klog.Errorf("Failed to get endpoint and cluster: %v", err)
			c.JSON(http.StatusNotFound, gin.H{
				"error": err.Error(),
			})

			return
		}

		// Route based on cluster type
		var response *LogSourcesResponse

		switch cluster.Spec.Type {
		case v1.SSHClusterType:
			response, err = getRayLogSourcesWithEndpoint(cluster, deps.HTTPClient, workspace, name, endpoint)
		case v1.KubernetesClusterType:
			response, err = getK8sLogSources(cluster, endpoint, workspace, name)
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("unsupported cluster type: %s", cluster.Spec.Type),
			})

			return
		}

		if err != nil {
			klog.Errorf("Failed to get log sources: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("failed to get log sources: %v", err),
			})

			return
		}

		c.JSON(http.StatusOK, response)
	}
}

// handleGetLogs handles the log content retrieval request with streaming
func handleGetLogs(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Param("workspace")
		name := c.Param("name")
		replicaID := c.Param("replica_id")
		logType := c.Param("log_type")

		if workspace == "" || name == "" || replicaID == "" || logType == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "workspace, name, replica_id, and log_type are required",
			})

			return
		}

		// Parse query parameters
		linesStr := c.DefaultQuery("lines", "1000")

		lines, err := strconv.ParseInt(linesStr, 10, 64)
		if err != nil || lines <= 0 {
			lines = 1000
		}

		download := c.DefaultQuery("download", "false") == "true"

		// Get endpoint and cluster
		endpoint, cluster, err := getEndpointAndCluster(deps, workspace, name)
		if err != nil {
			klog.Errorf("Failed to get endpoint and cluster: %v", err)
			c.JSON(http.StatusNotFound, gin.H{
				"error": err.Error(),
			})

			return
		}

		// Setup unified streaming response headers
		filename := fmt.Sprintf("%s-%s-%s-%s.log", workspace, name, replicaID, logType)
		setupStreamingResponse(c, download, filename)

		// For HEAD requests, only return headers without body
		if c.Request.Method == "HEAD" {
			return
		}

		// Route based on cluster type and stream logs directly
		var streamErr error

		switch cluster.Spec.Type {
		case v1.SSHClusterType:
			streamErr = streamRayLogs(c, cluster, deps.HTTPClient, workspace, name, replicaID, logType, lines)
		case v1.KubernetesClusterType:
			streamErr = streamK8sLogs(c, cluster, deps.K8sClient, endpoint, replicaID, logType, lines)
		default:
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf("unsupported cluster type: %s", cluster.Spec.Type),
			})

			return
		}

		if streamErr != nil {
			klog.Errorf("Failed to stream logs: %v", streamErr)
			// Note: Cannot return JSON error after streaming has started
			// Connection will be terminated
		}
	}
}

// getEndpointAndCluster retrieves the endpoint and its cluster
func getEndpointAndCluster(deps *Dependencies, workspace, name string) (*v1.Endpoint, *v1.Cluster, error) {
	// Get endpoint
	endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(name),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
		},
	})
	if err != nil {
		return nil, nil, fmt.Errorf("failed to list endpoints: %v", err)
	}

	if len(endpoints) == 0 {
		return nil, nil, fmt.Errorf("endpoint not found")
	}

	endpoint := &endpoints[0]

	// Get cluster
	clusters, err := deps.Storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(endpoint.Metadata.Workspace),
			},
		},
	})

	if err != nil {
		return nil, nil, fmt.Errorf("failed to list clusters: %v", err)
	}

	if len(clusters) == 0 {
		return nil, nil, fmt.Errorf("cluster not found")
	}

	return endpoint, &clusters[0], nil
}

func getRayLogSourcesWithEndpoint(cluster *v1.Cluster, httpClient util.HTTPClient, workspace, endpointName string, endpoint *v1.Endpoint) (*LogSourcesResponse, error) {
	dashboardURL := cluster.Status.DashboardURL
	if dashboardURL == "" {
		return nil, fmt.Errorf("dashboard_url not found in cluster")
	}

	// Call Ray Dashboard API to get applications
	url := fmt.Sprintf("%s/api/serve/applications/", dashboardURL)

	resp, err := httpClient.Get(url)
	if err != nil {
		return nil, fmt.Errorf("failed to call Ray Dashboard API: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("Ray Dashboard API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %v", err)
	}

	var rayApps RayApplicationsResponse
	if err := json.Unmarshal(body, &rayApps); err != nil {
		return nil, fmt.Errorf("failed to parse Ray applications response: %v", err)
	}

	// Find the application for this endpoint
	appName := fmt.Sprintf("%s_%s", workspace, endpointName)
	app, ok := rayApps.Applications[appName]

	if !ok {
		return nil, fmt.Errorf("application %s not found in Ray Dashboard", appName)
	}

	// Build response
	response := &LogSourcesResponse{
		Deployments: []DeploymentInfo{},
	}

	for deploymentName, deployment := range app.Deployments {
		replicas := []ReplicaInfo{}
		actors := []ActorInfo{}

		for _, replica := range deployment.Replicas {
			logs := []LogInfo{
				{
					Type:        logTypeApplication,
					URL:         rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeApplication, "", false),
					DownloadURL: rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeApplication, "", true),
					ActorID:     replica.ActorID,
				},
				{
					Type:        logTypeStderr,
					URL:         rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeStderr, "", false),
					DownloadURL: rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeStderr, "", true),
					ActorID:     replica.ActorID,
				},
				{
					Type:        logTypeStdout,
					URL:         rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeStdout, "", false),
					DownloadURL: rayLogURL(workspace, endpointName, replica.ReplicaID, logTypeStdout, "", true),
					ActorID:     replica.ActorID,
				},
			}

			replicas = append(replicas, ReplicaInfo{
				ReplicaID: replica.ReplicaID,
				Logs:      logs,
			})

			if deploymentName == "PDCollocatedBackend" {
				actors = append(actors, buildPDRoleActorSources(
					dashboard.NewDashboardService(dashboardURL), endpoint, workspace, endpointName, replica.ReplicaID,
				)...)
			}
		}

		if len(replicas) == 0 {
			// State-API fallback failure is non-fatal here: log-sources should
			// still return whatever live deployments exist with empty replicas.
			// streamRayLogs takes the opposite stance and propagates the error,
			// because a failing log stream cannot return partial output.
			failed, err := dashboard.FindFailedActorForDeployment(dashboard.NewDashboardService(dashboardURL), appName, deploymentName)
			if err != nil {
				klog.Warningf("failed to look up DEAD actors for %s/%s: %v", appName, deploymentName, err)
			} else if failed != nil {
				replicas = append(replicas, buildFailedReplicaInfo(workspace, endpointName, dashboard.ReplicaShortIDFromActor(failed)))
			}
		}

		response.Deployments = append(response.Deployments, DeploymentInfo{
			Name:     deploymentName,
			Replicas: replicas,
			Actors:   actors,
		})
	}

	return response, nil
}

func buildPDRoleActorSources(svc dashboard.DashboardService, endpoint *v1.Endpoint, workspace, endpointName, replicaID string) []ActorInfo {
	roleCounts := pdRoleCounts(endpoint)
	if len(roleCounts) == 0 || replicaID == "" {
		return nil
	}

	actors := []ActorInfo{}

	for _, role := range []string{"prefill", "decode"} {
		count := roleCounts[role]
		for rank := 0; rank < count; rank++ {
			actorName := pdRoleActorName(workspace, replicaID, role, rank)
			actor, err := dashboard.FindActorByNamePreferAlive(svc, actorName)

			if err != nil {
				klog.Warningf("failed to look up PD role actor %s: %v", actorName, err)
				continue
			}

			if actor == nil || actor.ActorID == "" {
				continue
			}

			actors = append(actors, buildRayRoleActorInfo(
				workspace, endpointName, replicaID, role, rank, actorName, actor.ActorID,
			))
		}
	}

	return actors
}

func pdRoleCounts(endpoint *v1.Endpoint) map[string]int {
	if endpoint == nil || endpoint.Spec == nil || endpoint.Spec.Strategy != "pd" {
		return nil
	}

	if counts := pdRoleCountsFromDeploymentOptions(endpoint.Spec.DeploymentOptions); len(counts) > 0 {
		return counts
	}

	counts := map[string]int{}

	for _, role := range endpoint.Spec.Roles {
		if role.Name != "prefill" && role.Name != "decode" {
			continue
		}

		count := 1
		if role.Replicas != nil && role.Replicas.Num != nil && *role.Replicas.Num > 0 {
			count = *role.Replicas.Num
		}

		counts[role.Name] = count
	}

	if counts["prefill"] == 0 || counts["decode"] == 0 {
		return nil
	}

	return counts
}

func pdRoleCountsFromDeploymentOptions(deploymentOptions map[string]interface{}) map[string]int {
	backend, ok := mapValue(deploymentOptions["backend"])
	if !ok {
		return nil
	}

	group, ok := mapValue(backend["group"])
	if !ok {
		return nil
	}

	roleItems, ok := roleListValue(group["roles"])
	if !ok {
		return nil
	}

	counts := map[string]int{}

	for _, item := range roleItems {
		roleMap, ok := mapValue(item)
		if !ok {
			continue
		}

		name, _ := roleMap["name"].(string)
		if name != "prefill" && name != "decode" {
			continue
		}

		instances, ok := positiveIntValue(roleMap["instances"])
		if !ok {
			instances = 1
		}

		counts[name] = instances
	}

	if counts["prefill"] == 0 || counts["decode"] == 0 {
		return nil
	}

	return counts
}

func mapValue(v interface{}) (map[string]interface{}, bool) {
	m, ok := v.(map[string]interface{})

	return m, ok
}

func roleListValue(v interface{}) ([]interface{}, bool) {
	switch roles := v.(type) {
	case []interface{}:
		return roles, true
	case []map[string]interface{}:
		out := make([]interface{}, 0, len(roles))
		for _, role := range roles {
			out = append(out, role)
		}

		return out, true
	default:
		return nil, false
	}
}

func positiveIntValue(v interface{}) (int, bool) {
	switch value := v.(type) {
	case int:
		return positiveIntResult(value)
	case int32:
		return positiveIntResult(int(value))
	case int64:
		return positiveIntResult(int(value))
	case float64:
		if value != float64(int(value)) {
			return 0, false
		}

		return positiveIntResult(int(value))
	case json.Number:
		parsed, err := strconv.Atoi(value.String())
		if err != nil {
			return 0, false
		}

		return positiveIntResult(parsed)
	case string:
		parsed, err := strconv.Atoi(value)
		if err != nil {
			return 0, false
		}

		return positiveIntResult(parsed)
	default:
		return 0, false
	}
}

func positiveIntResult(value int) (int, bool) {
	if value <= 0 {
		return 0, false
	}

	return value, true
}

func pdRoleActorName(workspace, replicaID, role string, rank int) string {
	workspaceScope := ""
	if workspace != "" {
		workspaceScope = fmt.Sprintf("workspace:%s:", workspace)
	}

	return fmt.Sprintf("neutree:%sreplica:%s:role:%s:rank:%d",
		workspaceScope, replicaID, role, rank)
}

func buildRayRoleActorInfo(workspace, endpointName, replicaID, role string, rank int, actorName, actorID string) ActorInfo {
	rankValue := rank

	return ActorInfo{
		ReplicaID: replicaID,
		ActorID:   actorID,
		ActorName: actorName,
		Role:      role,
		Rank:      &rankValue,
		Logs: []LogInfo{
			buildRayRoleActorLogInfo(workspace, endpointName, replicaID, logTypeStderr, role, rank, actorName, actorID),
			buildRayRoleActorLogInfo(workspace, endpointName, replicaID, logTypeStdout, role, rank, actorName, actorID),
		},
	}
}

func buildRayRoleActorLogInfo(workspace, endpointName, replicaID, logType, role string, rank int, actorName, actorID string) LogInfo {
	return LogInfo{
		Type:        logType,
		URL:         rayLogURL(workspace, endpointName, replicaID, logType, actorID, false),
		DownloadURL: rayLogURL(workspace, endpointName, replicaID, logType, actorID, true),
		Role:        role,
		Rank:        &rank,
		ActorName:   actorName,
		ActorID:     actorID,
	}
}

func rayLogURL(workspace, endpointName, replicaID, logType, actorID string, download bool) string {
	base := fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/%s", workspace, endpointName, replicaID, logType)
	q := url.Values{}

	if actorID != "" {
		q.Set("actor_id", actorID)
	}

	if download {
		q.Set("download", "true")
	}

	if len(q) == 0 {
		return base
	}

	return base + "?" + q.Encode()
}

// buildFailedReplicaInfo synthesizes a ReplicaInfo for a DEAD actor
// recovered from the state API. Only stderr / stdout log types are
// exposed because the application log file path is not retained in
// the actor table once Ray Serve has removed the replica.
func buildFailedReplicaInfo(workspace, endpointName, replicaID string) ReplicaInfo {
	mkLog := func(t string) LogInfo {
		return LogInfo{
			Type:        t,
			URL:         fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/%s", workspace, endpointName, replicaID, t),
			DownloadURL: fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/%s?download=true", workspace, endpointName, replicaID, t),
		}
	}

	return ReplicaInfo{
		ReplicaID: replicaID,
		Failed:    true,
		Logs:      []LogInfo{mkLog(logTypeStderr), mkLog(logTypeStdout)},
	}
}

// getK8sLogSources gets log sources from Kubernetes cluster
func getK8sLogSources(cluster *v1.Cluster, endpoint *v1.Endpoint, workspace, endpointName string) (*LogSourcesResponse, error) {
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	// Get pods for this endpoint
	logInfos, err := util.GetEndpointLogsInfo(ctx, cluster, endpoint)
	if err != nil {
		return nil, fmt.Errorf("failed to get endpoint logs info: %v", err)
	}

	if len(logInfos) == 0 {
		return nil, fmt.Errorf("no pods found for endpoint %s", endpointName)
	}

	replicas := buildK8sReplicaLogSources(logInfos, workspace, endpointName)

	response := &LogSourcesResponse{
		Deployments: []DeploymentInfo{
			{
				Name:     "Backend",
				Replicas: replicas,
			},
		},
	}

	return response, nil
}

func buildK8sReplicaLogSources(logInfos []util.EndpointLogInfo, workspace, endpointName string) []ReplicaInfo {
	replicas := make([]ReplicaInfo, 0)
	indexByReplica := make(map[string]int)

	for _, info := range logInfos {
		replicaID := info.ReplicaID
		if replicaID == "" {
			replicaID = info.PodName
		}

		if _, exists := indexByReplica[replicaID]; !exists {
			indexByReplica[replicaID] = len(replicas)
			replicas = append(replicas, ReplicaInfo{ReplicaID: replicaID})
		}

		role, rank := info.Role, info.Rank
		if role == "" && rank == nil {
			role, rank = parseK8sContainerLogIdentity(info.ContainerName)
		}

		idx := indexByReplica[replicaID]
		replicas[idx].Logs = append(replicas[idx].Logs, LogInfo{
			Type:          "logs",
			URL:           k8sContainerLogURL(workspace, endpointName, replicaID, info.ContainerName, false),
			DownloadURL:   k8sContainerLogURL(workspace, endpointName, replicaID, info.ContainerName, true),
			ContainerName: info.ContainerName,
			Role:          role,
			Rank:          rank,
		})
	}

	return replicas
}

func k8sContainerLogURL(workspace, endpointName, replicaID, containerName string, download bool) string {
	base := fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/logs", workspace, endpointName, replicaID)
	q := url.Values{}

	if containerName != "" {
		q.Set("container", containerName)
	}

	if download {
		q.Set("download", "true")
	}

	if len(q) == 0 {
		return base
	}

	return base + "?" + q.Encode()
}

func parseK8sContainerLogIdentity(containerName string) (string, *int) {
	if containerName == "pd-router" {
		return "router", nil
	}

	role, suffix, ok := strings.Cut(containerName, "-")
	if !ok || (role != "prefill" && role != "decode") {
		return "", nil
	}

	rank, err := strconv.Atoi(suffix)
	if err != nil || rank < 0 {
		return "", nil
	}

	return role, &rank
}

// streamRayLogs streams logs from Ray Dashboard API directly to the response
func streamRayLogs(c *gin.Context, cluster *v1.Cluster, httpClient util.HTTPClient, workspace, endpointName, replicaID, logType string, lines int64) error {
	dashboardURL := cluster.Status.DashboardURL
	if dashboardURL == "" {
		return fmt.Errorf("dashboard_url not found in cluster")
	}

	if actorID := rayActorIDQuery(c); actorID != "" {
		switch logType {
		case logTypeStderr:
			return streamRayLogURL(c, httpClient, fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
				dashboardURL, actorID, lines))
		case logTypeStdout:
			return streamRayLogURL(c, httpClient, fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
				dashboardURL, actorID, lines))
		case logTypeApplication:
			return fmt.Errorf("application log is unavailable for explicit actor_id %s; use stderr or stdout", actorID)
		default:
			return fmt.Errorf("unsupported log type: %s", logType)
		}
	}

	// First, get the application info to find node_id, actor_id, and log_file_path
	appURL := fmt.Sprintf("%s/api/serve/applications/", dashboardURL)

	resp, err := httpClient.Get(appURL)
	if err != nil {
		return fmt.Errorf("failed to call Ray Dashboard API: %v", err)
	}

	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ray Dashboard API returned status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return fmt.Errorf("failed to read response body: %v", err)
	}

	var rayApps RayApplicationsResponse
	if err := json.Unmarshal(body, &rayApps); err != nil {
		return fmt.Errorf("failed to parse Ray applications response: %v", err)
	}

	// Find the replica
	appName := fmt.Sprintf("%s_%s", workspace, endpointName)
	app, ok := rayApps.Applications[appName]

	if !ok {
		return fmt.Errorf("application %s not found", appName)
	}

	var replica *RayReplica

	for _, deployment := range app.Deployments {
		for _, r := range deployment.Replicas {
			if r.ReplicaID == replicaID {
				replica = &r
				break
			}
		}

		if replica != nil {
			break
		}
	}

	// Build log URL based on log type. When the replica is not in the
	// live applications response (Ray Serve removed the failed actor),
	// fall back to the state API to recover the actor_id.
	var logURL string

	if replica != nil {
		switch logType {
		case logTypeApplication:
			// Remove leading slash from log file path if present
			logFilePath := replica.LogFilePath
			if len(logFilePath) > 0 && logFilePath[0] == '/' {
				logFilePath = logFilePath[1:]
			}

			logURL = fmt.Sprintf("%s/api/v0/logs/file?node_id=%s&filename=%s&lines=%d&format=text",
				dashboardURL, replica.NodeID, logFilePath, lines)
		case logTypeStderr:
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
				dashboardURL, replica.ActorID, lines)
		case logTypeStdout:
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
				dashboardURL, replica.ActorID, lines)
		default:
			return fmt.Errorf("unsupported log type: %s", logType)
		}
	} else {
		deploymentNames := make([]string, 0, len(app.Deployments))
		for n := range app.Deployments {
			deploymentNames = append(deploymentNames, n)
		}

		failedActor, err := dashboard.LookupFailedActorAcrossDeployments(
			dashboard.NewDashboardService(dashboardURL), appName, deploymentNames, replicaID,
		)
		if err != nil {
			return err
		}

		if failedActor == nil {
			return fmt.Errorf("replica %s not found", replicaID)
		}

		switch logType {
		case logTypeStderr:
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
				dashboardURL, failedActor.ActorID, lines)
		case logTypeStdout:
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
				dashboardURL, failedActor.ActorID, lines)
		case logTypeApplication:
			return fmt.Errorf("application log is unavailable for failed replica %s; use stderr or stdout", replicaID)
		default:
			return fmt.Errorf("unsupported log type: %s", logType)
		}
	}

	return streamRayLogURL(c, httpClient, logURL)
}

func rayActorIDQuery(c *gin.Context) string {
	if c == nil || c.Request == nil || c.Request.URL == nil {
		return ""
	}

	return c.Query("actor_id")
}

func streamRayLogURL(c *gin.Context, httpClient util.HTTPClient, logURL string) error {
	logResp, err := httpClient.Get(logURL)
	if err != nil {
		return fmt.Errorf("failed to fetch logs: %v", err)
	}
	defer logResp.Body.Close()

	if logResp.StatusCode != http.StatusOK {
		return fmt.Errorf("Ray logs API returned status %d", logResp.StatusCode)
	}

	// Stream logs directly to response without buffering in memory
	_, err = io.Copy(c.Writer, logResp.Body)
	if err != nil {
		return fmt.Errorf("failed to stream logs: %v", err)
	}

	return nil
}

// streamK8sLogs streams logs from Kubernetes cluster directly to the response
func streamK8sLogs(c *gin.Context, cluster *v1.Cluster, k8sClient util.K8sClient, endpoint *v1.Endpoint, replicaID, logType string, lines int64) error {
	if logType != "logs" {
		return fmt.Errorf("unsupported log type for Kubernetes: %s (only 'logs' is supported)", logType)
	}

	ctx := c.Request.Context()
	namespace := util.ClusterNamespace(cluster)

	// Get pod to determine container name
	pod, err := k8sClient.GetPod(ctx, cluster, namespace, replicaID)
	if err != nil {
		return fmt.Errorf("failed to get pod %s: %v", replicaID, err)
	}

	containerName, err := resolveK8sLogContainer(pod, c.Query("container"), endpoint)
	if err != nil {
		return err
	}

	// Configure log options
	logOptions := &corev1.PodLogOptions{
		Container:  containerName,
		Timestamps: true,
	}

	if lines > 0 {
		logOptions.TailLines = &lines
	}

	// Get log stream
	stream, err := k8sClient.GetPodLogs(ctx, cluster, namespace, replicaID, logOptions)
	if err != nil {
		return fmt.Errorf("failed to stream logs: %v", err)
	}
	defer stream.Close()

	// Stream logs directly to response without buffering in memory
	_, err = io.Copy(c.Writer, stream)
	if err != nil {
		return fmt.Errorf("failed to copy logs: %v", err)
	}

	return nil
}

func resolveK8sLogContainer(pod *corev1.Pod, requested string, endpoint *v1.Endpoint) (string, error) {
	if pod == nil {
		return "", fmt.Errorf("pod is nil")
	}

	if len(pod.Spec.Containers) == 0 {
		return "", fmt.Errorf("no containers found in pod %s", pod.Name)
	}

	if requested != "" {
		for _, container := range pod.Spec.Containers {
			if container.Name == requested {
				return requested, nil
			}
		}

		return "", fmt.Errorf("container %q not found in pod %s", requested, pod.Name)
	}

	if len(pod.Spec.Containers) == 1 {
		return pod.Spec.Containers[0].Name, nil
	}

	if endpoint != nil && endpoint.Spec != nil && endpoint.Spec.Strategy == "pd" {
		return "", fmt.Errorf("container query parameter is required for PD pod %s with %d containers", pod.Name, len(pod.Spec.Containers))
	}

	return pod.Spec.Containers[0].Name, nil
}
