package logs

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sort"
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

// LogInfo represents log information
type LogInfo struct {
	Type        string `json:"type"` // "application" | "stderr" | "stdout" | "logs"
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}

// RayApplicationsResponse represents Ray Dashboard API response structure.
//
// TODO(NEU-423 follow-up): these RayApplicationsResponse / RayApplication /
// RayDeployment / RayReplica types and the inline httpClient.Get for
// /api/serve/applications/ predate the rule that "Ray dashboard API types
// and calls live in internal/ray/dashboard/". The new failed-actor lookup
// follows the rule (see internal/ray/dashboard/actors.go); migrating these
// older types is out of scope for the bug fix and tracked separately to
// keep the change minimal.
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
			response, err = getRayLogSources(cluster, deps.HTTPClient, workspace, name)
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

// getRayLogSources gets log sources from Ray Dashboard API
func getRayLogSources(cluster *v1.Cluster, httpClient util.HTTPClient, workspace, endpointName string) (*LogSourcesResponse, error) {
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

		for _, replica := range deployment.Replicas {
			logs := []LogInfo{
				{
					Type:        "application",
					URL:         fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/application", workspace, endpointName, replica.ReplicaID),
					DownloadURL: fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/application?download=true", workspace, endpointName, replica.ReplicaID),
				},
				{
					Type:        "stderr",
					URL:         fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/stderr", workspace, endpointName, replica.ReplicaID),
					DownloadURL: fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/stderr?download=true", workspace, endpointName, replica.ReplicaID),
				},
				{
					Type:        "stdout",
					URL:         fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/stdout", workspace, endpointName, replica.ReplicaID),
					DownloadURL: fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/stdout?download=true", workspace, endpointName, replica.ReplicaID),
				},
			}
			replicas = append(replicas, ReplicaInfo{
				ReplicaID: replica.ReplicaID,
				Logs:      logs,
			})
		}

		if len(replicas) == 0 {
			// State-API fallback failure is non-fatal here: log-sources should
			// still return whatever live deployments exist with empty replicas.
			// streamRayLogs takes the opposite stance and propagates the error,
			// because a failing log stream cannot return partial output.
			failed, err := findFailedActorForDeployment(dashboard.NewDashboardService(dashboardURL), appName, deploymentName)
			if err != nil {
				klog.Warningf("failed to look up DEAD actors for %s/%s: %v", appName, deploymentName, err)
			} else if failed != nil {
				replicas = append(replicas, buildFailedReplicaInfo(workspace, endpointName, replicaShortIDFromActor(failed)))
			}
		}

		response.Deployments = append(response.Deployments, DeploymentInfo{
			Name:     deploymentName,
			Replicas: replicas,
		})
	}

	return response, nil
}

// extractReplicaShortID parses a Ray Serve actor name of the form
// "SERVE_REPLICA::<app>#<deployment>#<short_id>" and returns short_id.
// Returns "" if the name does not have at least one '#' separator or
// ends with one.
func extractReplicaShortID(actorName string) string {
	idx := strings.LastIndex(actorName, "#")
	if idx < 0 || idx+1 >= len(actorName) {
		return ""
	}

	return actorName[idx+1:]
}

// replicaShortIDFromActor returns the short_id parsed from actor.Name,
// falling back to actor_id when the name does not match the Ray Serve
// convention.
func replicaShortIDFromActor(a *dashboard.Actor) string {
	if id := extractReplicaShortID(a.Name); id != "" {
		return id
	}

	return a.ActorID
}

// listFailedActorsForDeployment fetches DEAD actors that belong to the
// given Ray Serve <app>:<deployment> from the dashboard state API.
//
// limit=100 is intentional: the goal is to give the user the most recent
// few failures, not the full history. A deployment that has flapped more
// than 100 times within Ray's actor-table retention window will trim the
// oldest records, which is acceptable.
func listFailedActorsForDeployment(svc dashboard.DashboardService, appName, deploymentName string) ([]dashboard.Actor, error) {
	className := fmt.Sprintf("ServeReplica:%s:%s", appName, deploymentName)
	resp, err := svc.ListActors([]dashboard.ActorFilter{
		{Key: "class_name", Predicate: "=", Value: className},
		{Key: "state", Predicate: "=", Value: "DEAD"},
	}, true, 100)

	if err != nil {
		return nil, fmt.Errorf("list actors %s: %w", className, err)
	}

	return resp.Data.Result.Result, nil
}

// findFailedActorForDeployment returns the most recently started DEAD actor
// for the deployment, or nil if none exist. "Most recent" is decided by
// the actor's start_time (unix ms from GCS ActorTableData); ties fall back
// to actor_id lexicographic order so the result is still deterministic.
func findFailedActorForDeployment(svc dashboard.DashboardService, appName, deploymentName string) (*dashboard.Actor, error) {
	actors, err := listFailedActorsForDeployment(svc, appName, deploymentName)
	if err != nil {
		return nil, err
	}

	if len(actors) == 0 {
		return nil, nil
	}

	pick := 0

	for i := 1; i < len(actors); i++ {
		switch {
		case actors[i].StartTime > actors[pick].StartTime:
			pick = i
		case actors[i].StartTime == actors[pick].StartTime && actors[i].ActorID > actors[pick].ActorID:
			pick = i
		}
	}

	a := actors[pick]

	return &a, nil
}

// findFailedActorByReplicaID returns the DEAD actor whose name encodes
// the requested replica_id, or nil if no DEAD actor matches.
func findFailedActorByReplicaID(svc dashboard.DashboardService, appName, deploymentName, replicaID string) (*dashboard.Actor, error) {
	actors, err := listFailedActorsForDeployment(svc, appName, deploymentName)
	if err != nil {
		return nil, err
	}

	for i := range actors {
		if extractReplicaShortID(actors[i].Name) == replicaID {
			return &actors[i], nil
		}
	}

	return nil, nil
}

// lookupFailedActorAcrossDeployments scans every deployment in the live
// Ray Serve applications response for a DEAD actor whose name encodes
// the requested replica_id. Returns the first match, or (nil, nil) when
// no DEAD actor matches in any deployment.
//
// Deployment names are sorted before iteration so multi-deployment apps
// (e.g. P/D, prefill+decode) get a deterministic search order. This
// matters less today (one deployment per endpoint) but pre-empts
// nondeterministic behavior the moment that assumption breaks.
func lookupFailedActorAcrossDeployments(
	svc dashboard.DashboardService,
	appName string,
	deployments map[string]RayDeployment,
	replicaID string,
) (*dashboard.Actor, error) {
	names := make([]string, 0, len(deployments))
	for n := range deployments {
		names = append(names, n)
	}

	sort.Strings(names)

	for _, deploymentName := range names {
		actor, err := findFailedActorByReplicaID(svc, appName, deploymentName, replicaID)
		if err != nil {
			return nil, err
		}

		if actor != nil {
			return actor, nil
		}
	}

	return nil, nil
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
		Logs:      []LogInfo{mkLog("stderr"), mkLog("stdout")},
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

	// Build response
	replicas := []ReplicaInfo{}

	for _, info := range logInfos {
		logs := []LogInfo{
			{
				Type:        "logs",
				URL:         fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/logs", workspace, endpointName, info.ReplicaID),
				DownloadURL: fmt.Sprintf("/api/v1/endpoints/%s/%s/logs/%s/logs?download=true", workspace, endpointName, info.ReplicaID),
			},
		}
		replicas = append(replicas, ReplicaInfo{
			ReplicaID: info.ReplicaID,
			Logs:      logs,
		})
	}

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

// streamRayLogs streams logs from Ray Dashboard API directly to the response
func streamRayLogs(c *gin.Context, cluster *v1.Cluster, httpClient util.HTTPClient, workspace, endpointName, replicaID, logType string, lines int64) error {
	dashboardURL := cluster.Status.DashboardURL
	if dashboardURL == "" {
		return fmt.Errorf("dashboard_url not found in cluster")
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
		case "application":
			// Remove leading slash from log file path if present
			logFilePath := replica.LogFilePath
			if len(logFilePath) > 0 && logFilePath[0] == '/' {
				logFilePath = logFilePath[1:]
			}

			logURL = fmt.Sprintf("%s/api/v0/logs/file?node_id=%s&filename=%s&lines=%d&format=text",
				dashboardURL, replica.NodeID, logFilePath, lines)
		case "stderr":
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
				dashboardURL, replica.ActorID, lines)
		case "stdout":
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
				dashboardURL, replica.ActorID, lines)
		default:
			return fmt.Errorf("unsupported log type: %s", logType)
		}
	} else {
		failedActor, err := lookupFailedActorAcrossDeployments(dashboard.NewDashboardService(dashboardURL), appName, app.Deployments, replicaID)
		if err != nil {
			return err
		}

		if failedActor == nil {
			return fmt.Errorf("replica %s not found", replicaID)
		}

		switch logType {
		case "stderr":
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
				dashboardURL, failedActor.ActorID, lines)
		case "stdout":
			logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
				dashboardURL, failedActor.ActorID, lines)
		case "application":
			return fmt.Errorf("application log is unavailable for failed replica %s; use stderr or stdout", replicaID)
		default:
			return fmt.Errorf("unsupported log type: %s", logType)
		}
	}

	// Fetch logs
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
	_ = endpoint // Reserved for future use

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

	// Find the main container (first non-init container)
	var containerName string
	if len(pod.Spec.Containers) > 0 {
		containerName = pod.Spec.Containers[0].Name
	} else {
		return fmt.Errorf("no containers found in pod %s", replicaID)
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
