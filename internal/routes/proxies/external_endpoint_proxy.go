package proxies

import (
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

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
	proxyGroup.POST("/test_connectivity", handleTestConnectivity())
}

// testConnectivityRequest is the request body for the test connectivity endpoint.
type testConnectivityRequest struct {
	Upstream *v1.ExternalEndpointUpstreamSpec `json:"upstream"`
	Auth     *v1.ExternalEndpointAuthSpec     `json:"auth,omitempty"`
}

// testConnectivityResponse is the response body for the test connectivity endpoint.
type testConnectivityResponse struct {
	Success   bool     `json:"success"`
	LatencyMs int64    `json:"latency_ms,omitempty"`
	Models    []string `json:"models,omitempty"`
	Error     string   `json:"error,omitempty"`
}

func handleTestConnectivity() gin.HandlerFunc {
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

		if req.Upstream == nil || req.Upstream.URL == "" {
			c.JSON(http.StatusBadRequest, testConnectivityResponse{
				Success: false,
				Error:   "upstream.url is required",
			})

			return
		}

		modelsURL := req.Upstream.URL + "/models"

		httpReq, err := http.NewRequestWithContext(c.Request.Context(), http.MethodGet, modelsURL, nil)
		if err != nil {
			c.JSON(http.StatusBadRequest, testConnectivityResponse{
				Success: false,
				Error:   "invalid upstream URL: " + err.Error(),
			})

			return
		}

		if req.Auth != nil {
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
