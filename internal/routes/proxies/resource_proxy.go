package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// FieldSelector defines which fields to include or exclude in responses
type FieldSelector struct {
	// Fields to exclude from response, using dot-separated paths as keys
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

// extractExcludeFieldsFromTag extracts fields marked with api:"-" tag from a struct type
// Returns a map of JSON paths to exclude
func extractExcludeFieldsFromTag(t reflect.Type) map[string]struct{} {
	excludeFields := make(map[string]struct{})
	extractFieldsRecursive(t, "", excludeFields)

	return excludeFields
}

// extractFieldsRecursive recursively extracts fields with api:"-" tag
func extractFieldsRecursive(t reflect.Type, prefix string, excludeFields map[string]struct{}) {
	// Handle pointer types
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	// Only process struct types
	if t.Kind() != reflect.Struct {
		return
	}

	for i := 0; i < t.NumField(); i++ {
		field := t.Field(i)

		// Skip unexported fields
		if !field.IsExported() {
			continue
		}

		// Get json tag to determine field name in JSON
		jsonTag := field.Tag.Get("json")
		if jsonTag == "" || jsonTag == "-" {
			continue
		}

		// Parse json tag
		jsonName := strings.Split(jsonTag, ",")[0]

		// Build the full path
		var fieldPath string
		if prefix == "" {
			fieldPath = jsonName
		} else {
			fieldPath = prefix + "." + jsonName
		}

		// Check if this field has api:"-" tag
		apiTag := field.Tag.Get("api")
		if apiTag == "-" {
			excludeFields[fieldPath] = struct{}{}
		}

		// Recursively process nested structs
		fieldType := field.Type
		if fieldType.Kind() == reflect.Ptr {
			fieldType = fieldType.Elem()
		}

		if fieldType.Kind() == reflect.Struct {
			extractFieldsRecursive(fieldType, fieldPath, excludeFields)
		}
	}
}

// mergeExcludedFields merges excluded fields from source into target
// Only merges fields that are in excludeFields and missing in target
func mergeExcludedFields(target, source map[string]interface{}, excludeFields map[string]struct{}) {
	mergeExcludedFieldsRecursive(target, source, excludeFields, "")
}

// mergeExcludedFieldsRecursive recursively merges excluded fields
func mergeExcludedFieldsRecursive(target, source map[string]interface{}, excludeFields map[string]struct{}, currentPath string) {
	for key, sourceValue := range source {
		fieldPath := key
		if currentPath != "" {
			fieldPath = currentPath + "." + key
		}

		// Check if this field is an excluded field
		if _, isExcluded := excludeFields[fieldPath]; isExcluded {
			// If target doesn't have this field or it's empty, merge from source
			if targetValue, exists := target[key]; !exists || isEmptyValue(targetValue) {
				target[key] = sourceValue
			}

			continue
		}

		// Recursively merge nested objects
		if sourceMap, ok := sourceValue.(map[string]interface{}); ok {
			if targetMap, ok := target[key].(map[string]interface{}); ok {
				mergeExcludedFieldsRecursive(targetMap, sourceMap, excludeFields, fieldPath)
			} else if !ok && target[key] == nil {
				// Target has this key but it's nil, create a new map and merge
				target[key] = make(map[string]interface{})
				if targetMap, ok := target[key].(map[string]interface{}); ok {
					mergeExcludedFieldsRecursive(targetMap, sourceMap, excludeFields, fieldPath)
				}
			}
		}
	}
}

// isEmptyValue checks if a value is considered empty
func isEmptyValue(v interface{}) bool {
	if v == nil {
		return true
	}

	switch val := v.(type) {
	case string:
		return val == ""
	case map[string]interface{}:
		return len(val) == 0
	case []interface{}:
		return len(val) == 0
	default:
		return false
	}
}

// isSoftDeleteRequest checks if the request is a soft delete operation
// A soft delete is identified by the presence of a non-empty deletion_timestamp field in metadata
func isSoftDeleteRequest(requestBody map[string]interface{}) bool {
	// Check for metadata.deletion_timestamp
	if metadata, ok := requestBody["metadata"].(map[string]interface{}); ok {
		if deletionTimestamp, exists := metadata["deletion_timestamp"]; exists {
			// Check if deletion_timestamp is being set (not nil or empty)
			if deletionTimestamp != nil && deletionTimestamp != "" {
				return true
			}
		}
	}

	return false
}

// buildSelectParam builds PostgREST select parameter for excluded fields
// For example, if excludeFields contains "spec.credentials", it returns "spec"
func buildSelectParam(excludeFields map[string]struct{}) string {
	if len(excludeFields) == 0 {
		return ""
	}

	// Collect top-level fields that contain excluded nested fields
	fieldsMap := make(map[string]struct{})

	for fieldPath := range excludeFields {
		parts := strings.Split(fieldPath, ".")
		if len(parts) > 0 {
			fieldsMap[parts[0]] = struct{}{}
		}
	}

	// Convert to slice
	fields := make([]string, 0, len(fieldsMap))
	for field := range fieldsMap {
		fields = append(fields, field)
	}

	return strings.Join(fields, ",")
}

// queryParamsToFilters converts URL query params to storage.Filter array
func queryParamsToFilters(queryParams url.Values) []storage.Filter {
	filters := make([]storage.Filter, 0)

	// PostgREST reserved parameters that should not be treated as filters
	reservedParams := map[string]struct{}{
		"select": {},
		"order":  {},
		"limit":  {},
		"offset": {},
	}

	for key, values := range queryParams {
		if len(values) == 0 {
			continue
		}

		// Skip PostgREST reserved parameters
		if _, isReserved := reservedParams[key]; isReserved {
			continue
		}

		// PostgREST uses format like: column=operator.value or column=eq.value
		// For example: id=eq.123 or metadata->>name=eq.my-registry
		parts := strings.SplitN(values[0], ".", 2)
		if len(parts) == 2 {
			filters = append(filters, storage.Filter{
				Column:   key,
				Operator: parts[0],
				Value:    parts[1],
			})
		} else {
			// Default to eq operator if not specified
			filters = append(filters, storage.Filter{
				Column:   key,
				Operator: "eq",
				Value:    values[0],
			})
		}
	}

	return filters
}

// fetchCurrentResource fetches current resource from storage with only excluded fields
func fetchCurrentResource(
	storage storage.Storage, tableName string, queryParams url.Values, excludeFields map[string]struct{},
) (map[string]interface{}, error) {
	// Build select parameter to only fetch fields that contain excluded nested fields
	selectParam := buildSelectParam(excludeFields)
	if selectParam == "" {
		return nil, nil
	}

	// Convert query params to filters
	filters := queryParamsToFilters(queryParams)
	if len(filters) == 0 {
		return nil, fmt.Errorf("no filters provided in query params")
	}

	klog.V(4).Infof("Fetching current resource from table: %s with filters: %+v, select: %s", tableName, filters, selectParam)

	// Query using storage SDK (uses service_role token)
	var resources []map[string]interface{}
	if err := storage.GenericQuery(tableName, selectParam, filters, &resources); err != nil {
		return nil, fmt.Errorf("failed to query resource: %w", err)
	}

	if len(resources) == 0 {
		return nil, fmt.Errorf("resource not found")
	}

	// Return first resource
	return resources[0], nil
}

// CreateStructProxyHandler creates a proxy handler that filters response based on struct api tags
// The struct type T is used only for configuration extraction at initialization time
// At runtime, responses are filtered using map-based JSON filtering for performance
func CreateStructProxyHandler[T any](deps *Dependencies, tableName string) gin.HandlerFunc {
	// Extract exclude fields from struct type at initialization time (compile-time type safety)
	var zero T
	excludeFields := extractExcludeFieldsFromTag(reflect.TypeOf(zero))

	return func(c *gin.Context) {
		// Handle PATCH requests with excluded fields backfilling
		if c.Request.Method == "PATCH" && len(excludeFields) > 0 {
			handlePatchWithBackfill(c, deps, tableName, excludeFields)
			return
		}

		// Create proxy handler
		proxyHandler := CreateProxyHandler(deps.StorageAccessURL, tableName, CreatePostgrestAuthModifier(c))

		// If no field filtering is needed, use proxy directly
		if len(excludeFields) == 0 {
			proxyHandler(c)
			return
		}

		// Need to intercept response for field filtering
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
			filteredBody, err := filterResponseBody(io.NopCloser(responseWriter.body), excludeFields)
			if err != nil {
				c.Writer = responseWriter.ResponseWriter
				c.JSON(http.StatusInternalServerError, gin.H{
					"error": fmt.Sprintf("Failed to filter response: %v", err),
				})

				return
			}

			// Write filtered response
			c.Writer = responseWriter.ResponseWriter
			// Let c.Data re-calculate Content-Length
			c.Writer.Header().Del("Content-Length")
			c.Data(responseWriter.statusCode, "application/json", filteredBody)
		} else {
			// For error responses, write as-is
			c.Writer = responseWriter.ResponseWriter
			c.Data(responseWriter.statusCode, c.Writer.Header().Get("Content-Type"), responseWriter.body.Bytes())
		}
	}
}

// handlePatchWithBackfill handles PATCH requests with excluded fields backfilling
func handlePatchWithBackfill(c *gin.Context, deps *Dependencies, tableName string, excludeFields map[string]struct{}) {
	// Read request body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Failed to read request body: %v", err),
		})

		return
	}

	c.Request.Body.Close()

	// Parse request body
	var requestBody map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &requestBody); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"error": fmt.Sprintf("Failed to parse request body: %v", err),
		})

		return
	}

	// Skip backfill for soft delete operations
	if isSoftDeleteRequest(requestBody) {
		// Restore request body and forward directly to PostgREST
		c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
		c.Request.ContentLength = int64(len(bodyBytes))
		c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))

		proxyHandler := CreateProxyHandler(deps.StorageAccessURL, tableName, CreatePostgrestAuthModifier(c))
		proxyHandler(c)

		return
	}

	// Fetch current resource from storage (uses service_role token)
	currentResource, err := fetchCurrentResource(
		deps.Storage,
		tableName,
		c.Request.URL.Query(),
		excludeFields,
	)
	if err != nil {
		klog.Warningf("Failed to fetch current resource for backfill: %v", err)
		// Continue without backfill if fetch fails (resource might not exist yet)
	} else if currentResource != nil {
		// Merge excluded fields from current resource into request body
		mergeExcludedFields(requestBody, currentResource, excludeFields)
		klog.V(4).Infof("Backfilled excluded fields for PATCH request")
	}

	// Re-serialize request body
	modifiedBodyBytes, err := json.Marshal(requestBody)
	if err != nil {
		c.JSON(http.StatusInternalServerError, gin.H{
			"error": fmt.Sprintf("Failed to marshal request body: %v", err),
		})

		return
	}

	// Replace request body
	c.Request.Body = io.NopCloser(bytes.NewReader(modifiedBodyBytes))
	c.Request.ContentLength = int64(len(modifiedBodyBytes))
	c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBodyBytes)))

	// Forward to PostgREST
	proxyHandler := CreateProxyHandler(deps.StorageAccessURL, tableName, CreatePostgrestAuthModifier(c))
	proxyHandler(c)
}
