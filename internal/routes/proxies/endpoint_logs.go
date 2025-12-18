package proxies

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// LogSourcesResponse represents the response for log sources discovery
type LogSourcesResponse struct {
	Deployments []DeploymentInfo `json:"deployments"`
}

// DeploymentInfo represents deployment information
type DeploymentInfo struct {
	Name     string        `json:"name"`
	Replicas []ReplicaInfo `json:"replicas"`
}

// ReplicaInfo represents replica information
type ReplicaInfo struct {
	ReplicaID string    `json:"replica_id"`
	Logs      []LogInfo `json:"logs"`
}

// LogInfo represents log information
type LogInfo struct {
	Type        string `json:"type"` // "application" | "stderr" | "stdout" | "logs"
	URL         string `json:"url"`
	DownloadURL string `json:"download_url"`
}

// RayApplicationsResponse represents Ray Dashboard API response structure
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
			response, err = getRayLogSources(cluster, workspace, name)
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
			streamErr = streamRayLogs(c, cluster, workspace, name, replicaID, logType, lines)
		case v1.KubernetesClusterType:
			streamErr = streamK8sLogs(c, cluster, endpoint, replicaID, logType, lines)
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
func getRayLogSources(cluster *v1.Cluster, workspace, endpointName string) (*LogSourcesResponse, error) {
	dashboardURL := cluster.Status.DashboardURL
	if dashboardURL == "" {
		return nil, fmt.Errorf("dashboard_url not found in cluster")
	}

	// Call Ray Dashboard API to get applications
	url := fmt.Sprintf("%s/api/serve/applications/", dashboardURL)

	// nolint:gosec // URL is from trusted cluster configuration
	resp, err := http.Get(url)
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

		response.Deployments = append(response.Deployments, DeploymentInfo{
			Name:     deploymentName,
			Replicas: replicas,
		})
	}

	return response, nil
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
func streamRayLogs(c *gin.Context, cluster *v1.Cluster, workspace, endpointName, replicaID, logType string, lines int64) error {
	dashboardURL := cluster.Status.DashboardURL
	if dashboardURL == "" {
		return fmt.Errorf("dashboard_url not found in cluster")
	}

	// First, get the application info to find node_id, actor_id, and log_file_path
	appURL := fmt.Sprintf("%s/api/serve/applications/", dashboardURL)

	// nolint:gosec // URL is from trusted cluster configuration
	resp, err := http.Get(appURL)
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

	if replica == nil {
		return fmt.Errorf("replica %s not found", replicaID)
	}

	// Build log URL based on log type
	var logURL string

	switch logType {
	case "application":
		logURL = fmt.Sprintf("%s/api/v0/logs/file?node_id=%s&filename=%s&lines=%d&format=text",
			dashboardURL, replica.NodeID, replica.LogFilePath, lines)
	case "stderr":
		logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=err&lines=%d&format=text",
			dashboardURL, replica.ActorID, lines)
	case "stdout":
		logURL = fmt.Sprintf("%s/api/v0/logs/file?actor_id=%s&suffix=out&lines=%d&format=text",
			dashboardURL, replica.ActorID, lines)
	default:
		return fmt.Errorf("unsupported log type: %s", logType)
	}

	// Fetch logs
	// nolint:gosec // URL is constructed from trusted cluster configuration
	logResp, err := http.Get(logURL)
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
func streamK8sLogs(c *gin.Context, cluster *v1.Cluster, endpoint *v1.Endpoint, replicaID, logType string, lines int64) error {
	_ = endpoint // Reserved for future use

	if logType != "logs" {
		return fmt.Errorf("unsupported log type for Kubernetes: %s (only 'logs' is supported)", logType)
	}

	ctx := c.Request.Context()

	clientset, err := util.GetClientSetFromCluster(cluster)
	if err != nil {
		return fmt.Errorf("failed to get kubernetes clientset: %v", err)
	}

	namespace := util.ClusterNamespace(cluster)

	// Get pod to determine container name
	pod, err := clientset.CoreV1().Pods(namespace).Get(ctx, replicaID, metav1.GetOptions{})
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
	req := clientset.CoreV1().Pods(namespace).GetLogs(replicaID, logOptions)

	stream, err := req.Stream(ctx)
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
