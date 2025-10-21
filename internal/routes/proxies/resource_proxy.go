package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
)

// FieldSelector defines which fields to include or exclude in responses
type FieldSelector struct {
	// Fields to exclude from response, using dot-separated paths as keys
	// Example: map[string]struct{}{"status.sk_value": {}}
	ExcludeFields map[string]struct{}
}

// MethodConfig configures behavior for a specific HTTP method
type MethodConfig struct {
	// Whether this method is enabled
	Enabled bool

	// Field selection configuration for response filtering
	FieldSelector *FieldSelector

	// Optional custom handler, overrides default proxy behavior if set
	CustomHandler gin.HandlerFunc
}

// ResourceProxyConfig defines configuration for proxying a resource
type ResourceProxyConfig struct {
	// Resource name for routing
	ResourceName string

	// PostgREST table or view name
	TableName string

	// Configuration per HTTP method
	Methods map[string]*MethodConfig
}

// filterJSONFields removes fields from a JSON object based on dot-separated paths
func filterJSONFields(data interface{}, excludeFields map[string]struct{}) interface{} {
	if len(excludeFields) == 0 {
		return data
	}

	switch v := data.(type) {
	case map[string]interface{}:
		return filterObject(v, excludeFields, "")
	case []interface{}:
		result := make([]interface{}, len(v))
		for i, item := range v {
			result[i] = filterJSONFields(item, excludeFields)
		}
		return result
	default:
		return data
	}
}

// filterObject filters a JSON object recursively
func filterObject(obj map[string]interface{}, excludeFields map[string]struct{}, currentPath string) map[string]interface{} {
	result := make(map[string]interface{})

	for key, value := range obj {
		fieldPath := key
		if currentPath != "" {
			fieldPath = currentPath + "." + key
		}

		// Check if this field should be excluded using O(1) map lookup
		if _, shouldExclude := excludeFields[fieldPath]; shouldExclude {
			continue
		}

		// Recursively filter nested objects and arrays
		switch v := value.(type) {
		case map[string]interface{}:
			result[key] = filterObject(v, excludeFields, fieldPath)
		case []interface{}:
			filtered := make([]interface{}, len(v))
			for i, item := range v {
				if itemObj, ok := item.(map[string]interface{}); ok {
					filtered[i] = filterObject(itemObj, excludeFields, fieldPath)
				} else {
					filtered[i] = item
				}
			}
			result[key] = filtered
		default:
			result[key] = value
		}
	}

	return result
}

// filterResponseBody reads the response body, filters fields, and returns the modified body
func filterResponseBody(body io.ReadCloser, excludeFields map[string]struct{}) ([]byte, error) {
	defer body.Close()

	bodyBytes, err := io.ReadAll(body)
	if err != nil {
		return nil, fmt.Errorf("failed to read response body: %w", err)
	}

	if len(bodyBytes) == 0 {
		return bodyBytes, nil
	}

	var data interface{}
	if err := json.Unmarshal(bodyBytes, &data); err != nil {
		return nil, fmt.Errorf("failed to unmarshal response body: %w", err)
	}

	filtered := filterJSONFields(data, excludeFields)

	filteredBytes, err := json.Marshal(filtered)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal filtered response: %w", err)
	}

	return filteredBytes, nil
}

// CreateResourceProxyHandler creates a proxy handler based on ResourceProxyConfig
func CreateResourceProxyHandler(deps *Dependencies, config *ResourceProxyConfig) gin.HandlerFunc {
	return func(c *gin.Context) {
		method := c.Request.Method

		// Check if method is configured
		methodConfig, exists := config.Methods[method]
		if !exists || methodConfig == nil {
			c.JSON(http.StatusMethodNotAllowed, gin.H{
				"error": fmt.Sprintf("Method %s is not allowed for resource %s", method, config.ResourceName),
			})
			return
		}

		// Check if method is enabled
		if !methodConfig.Enabled {
			c.JSON(http.StatusMethodNotAllowed, gin.H{
				"error": fmt.Sprintf("Method %s is not allowed for resource %s", method, config.ResourceName),
			})
			return
		}

		// If custom handler is provided, use it
		if methodConfig.CustomHandler != nil {
			methodConfig.CustomHandler(c)
			return
		}

		// Use default proxy behavior with optional field filtering
		handleResourceProxyWithFilter(c, deps, config.TableName, methodConfig.FieldSelector)
	}
}

// handleResourceProxyWithFilter proxies the request to PostgREST with optional response filtering
func handleResourceProxyWithFilter(c *gin.Context, deps *Dependencies, tableName string, fieldSelector *FieldSelector) {
	// Create proxy handler
	proxyHandler := CreateProxyHandler(deps.StorageAccessURL, tableName, nil)

	// If no field filtering is needed, use proxy directly
	if fieldSelector == nil || len(fieldSelector.ExcludeFields) == 0 {
		proxyHandler(c)
		return
	}

	// Need to intercept response for field filtering
	// Create a custom ResponseWriter to capture the response
	responseWriter := &responseCapture{
		ResponseWriter: c.Writer,
		body:           &bytes.Buffer{},
		statusCode:     http.StatusOK,
	}
	c.Writer = responseWriter

	// Execute proxy
	proxyHandler(c)

	// Filter the response body if it's a successful response
	if responseWriter.statusCode >= 200 && responseWriter.statusCode < 300 {
		filteredBody, err := filterResponseBody(io.NopCloser(responseWriter.body), fieldSelector.ExcludeFields)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": fmt.Sprintf("Failed to filter response: %v", err),
			})
			return
		}

		// Write filtered response
		c.Writer = responseWriter.ResponseWriter
		// Remove Content-Length header since body length has changed
		c.Writer.Header().Del("Content-Length")
		c.Data(responseWriter.statusCode, "application/json", filteredBody)
	} else {
		// For error responses, write as-is
		c.Writer = responseWriter.ResponseWriter
		c.Data(responseWriter.statusCode, c.Writer.Header().Get("Content-Type"), responseWriter.body.Bytes())
	}
}

// responseCapture captures the response body and status code
type responseCapture struct {
	gin.ResponseWriter
	body       *bytes.Buffer
	statusCode int
}

func (w *responseCapture) Write(b []byte) (int, error) {
	return w.body.Write(b)
}

func (w *responseCapture) WriteHeader(statusCode int) {
	w.statusCode = statusCode
}

func (w *responseCapture) WriteString(s string) (int, error) {
	return w.body.WriteString(s)
}
