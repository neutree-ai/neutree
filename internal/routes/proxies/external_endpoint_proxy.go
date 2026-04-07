package proxies

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const defaultWorkspace = "default"

// RegisterExternalEndpointRoutes registers external endpoint routes
// The auth.credential field is masked in API responses (api:"-" tag)
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterExternalEndpointRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/external_endpoints")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ExternalEndpoint](deps, storage.EXTERNAL_ENDPOINT_TABLE)

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)

	// Test connectivity endpoint
	proxyGroup.POST("/test_connectivity", handleTestConnectivity(deps))
}

// testConnectivityRequest is the request body for the test connectivity endpoint.
// Exactly one of Upstream or EndpointRef must be set.
type testConnectivityRequest struct {
	Upstream    *v1.ExternalEndpointUpstreamSpec `json:"upstream,omitempty"`
	Auth        *v1.ExternalEndpointAuthSpec     `json:"auth,omitempty"`
	EndpointRef *string                          `json:"endpoint_ref,omitempty"`
	Workspace   *string                          `json:"workspace,omitempty"`
	// Name is the external endpoint name, used to backfill credentials in edit mode
	// when the auth credential is not provided in the request.
	Name *string `json:"name,omitempty"`
}

// testConnectivityResponse is the response body for the test connectivity endpoint.
type testConnectivityResponse struct {
	Success   bool     `json:"success"`
	LatencyMs int64    `json:"latency_ms,omitempty"`
	Models    []string `json:"models,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func handleTestConnectivity(deps *Dependencies) gin.HandlerFunc {
	client := &http.Client{
		Timeout: 15 * time.Second,
		Transport: &http.Transport{
			TLSClientConfig: &tls.Config{
				MinVersion: tls.VersionTLS12,
			},
		},
	}

	return func(c *gin.Context) {
		var req testConnectivityRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, testConnectivityResponse{
				Success: false,
				Error:   "invalid request body: " + err.Error(),
			})

			return
		}

		modelsURL, urlErr := resolveModelsURL(deps, &req)
		if urlErr != nil {
			c.JSON(http.StatusBadRequest, testConnectivityResponse{
				Success: false,
				Error:   urlErr.Error(),
			})

			return
		}

		httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, modelsURL, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, testConnectivityResponse{
				Success: false,
				Error:   "invalid upstream URL: " + err.Error(),
			})

			return
		}

		// Backfill credential from stored EE when auth credential is empty (edit mode)
		if req.Auth != nil && req.Auth.Credential == "" && req.Name != nil && *req.Name != "" {
			backfillAuthCredential(deps, &req)
		}

		if req.Auth != nil && req.Auth.Credential != "" {
			httpReq.Header.Set("Authorization", req.Auth.AuthHeaderValue())
		}

		start := time.Now()
		resp, err := client.Do(httpReq)
		latencyMs := time.Since(start).Milliseconds()

		if err != nil {
			c.JSON(http.StatusOK, testConnectivityResponse{
				Success:   false,
				LatencyMs: latencyMs,
				Error:     "connection failed: " + err.Error(),
			})

			return
		}

		defer resp.Body.Close()

		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20)) // 1MB limit

		if resp.StatusCode != http.StatusOK {
			errMsg := fmt.Sprintf("upstream returned HTTP %d", resp.StatusCode)
			if len(body) > 0 {
				errMsg += ": " + string(body)
			}

			c.JSON(http.StatusOK, testConnectivityResponse{
				Success:   false,
				LatencyMs: latencyMs,
				Error:     errMsg,
			})

			return
		}

		// Validate OpenAI-compatible model list response
		models, parseErr := parseModelIDs(body)
		if parseErr != nil {
			c.JSON(http.StatusOK, testConnectivityResponse{
				Success:   false,
				LatencyMs: latencyMs,
				Error:     "upstream response is not a valid OpenAI-compatible model list: " + parseErr.Error(),
			})

			return
		}

		c.JSON(http.StatusOK, testConnectivityResponse{
			Success:   true,
			LatencyMs: latencyMs,
			Models:    models,
		})
	}
}

// resolveModelsURL builds the /models URL to probe.
// For external upstreams: {url}/models  (user URL already includes /v1)
// For endpoint refs: {scheme}://{host}:{port}/{workspace}/{name}/v1/models
func resolveModelsURL(deps *Dependencies, req *testConnectivityRequest) (string, error) {
	hasUpstream := req.Upstream != nil && req.Upstream.URL != ""
	hasEndpointRef := req.EndpointRef != nil && *req.EndpointRef != ""

	if !hasUpstream && !hasEndpointRef {
		return "", fmt.Errorf("either upstream.url or endpoint_ref is required")
	}

	if hasUpstream && hasEndpointRef {
		return "", fmt.Errorf("upstream and endpoint_ref are mutually exclusive")
	}

	if hasUpstream {
		return req.Upstream.URL + "/models", nil
	}

	// Resolve endpoint ref
	workspace := defaultWorkspace
	if req.Workspace != nil && *req.Workspace != "" {
		workspace = *req.Workspace
	}

	endpointName := *req.EndpointRef

	endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(endpointName)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to look up endpoint %s: %w", endpointName, err)
	}

	if len(endpoints) == 0 {
		return "", fmt.Errorf("endpoint %s not found in workspace %s", endpointName, workspace)
	}

	ep := &endpoints[0]

	clusters, err := deps.Storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(ep.Spec.Cluster)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil {
		return "", fmt.Errorf("failed to look up cluster %s: %w", ep.Spec.Cluster, err)
	}

	if len(clusters) == 0 {
		return "", fmt.Errorf("cluster %s not found for endpoint %s", ep.Spec.Cluster, endpointName)
	}

	scheme, host, port, err := util.GetClusterServeAddress(&clusters[0])
	if err != nil {
		return "", fmt.Errorf("failed to get cluster serve address: %w", err)
	}

	return fmt.Sprintf("%s://%s:%d/%s/%s/v1/models", scheme, host, port, workspace, endpointName), nil
}

// backfillAuthCredential fetches the stored external endpoint and backfills
// the auth credential when it's missing from the request (edit mode scenario).
func backfillAuthCredential(deps *Dependencies, req *testConnectivityRequest) {
	workspace := defaultWorkspace
	if req.Workspace != nil && *req.Workspace != "" {
		workspace = *req.Workspace
	}

	ees, err := deps.Storage.ListExternalEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{Column: "metadata->name", Operator: "eq", Value: strconv.Quote(*req.Name)},
			{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote(workspace)},
		},
	})
	if err != nil || len(ees) == 0 {
		return
	}

	ee := &ees[0]
	if ee.Spec == nil {
		return
	}

	for _, entry := range ee.Spec.Upstreams {
		if entry.Auth == nil || entry.Auth.Credential == "" {
			continue
		}

		if req.Upstream != nil && entry.Upstream != nil && req.Upstream.URL == entry.Upstream.URL {
			req.Auth.Credential = entry.Auth.Credential
			return
		}
	}
}

// parseModelIDs validates and extracts model IDs from an OpenAI-compatible /models response.
// Returns error if body is not valid JSON, missing "data" array, or contains no models.
func parseModelIDs(body []byte) ([]string, error) {
	var raw map[string]json.RawMessage
	if err := json.Unmarshal(body, &raw); err != nil {
		return nil, fmt.Errorf("invalid JSON: %w", err)
	}

	dataRaw, ok := raw["data"]
	if !ok {
		return nil, fmt.Errorf("missing \"data\" field")
	}

	var data []struct {
		ID string `json:"id"`
	}

	if err := json.Unmarshal(dataRaw, &data); err != nil {
		return nil, fmt.Errorf("\"data\" is not a valid array: %w", err)
	}

	ids := make([]string, 0, len(data))

	for _, m := range data {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}

	if len(ids) == 0 {
		return nil, fmt.Errorf("no models found in response")
	}

	return ids, nil
}
