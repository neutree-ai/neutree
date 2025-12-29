package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/url"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// BodyContext holds parsed request body information
type BodyContext struct {
	BodyBytes []byte
	BodyMap   map[string]interface{}
}

// ExtractBody reads and parses the request body as JSON
// The request body is consumed and should be restored using RestoreBody if needed
func ExtractBody(c *gin.Context) (*BodyContext, error) {
	// Read body
	bodyBytes, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, fmt.Errorf("failed to read request body: %w", err)
	}
	c.Request.Body.Close()

	// Parse as JSON
	var bodyMap map[string]interface{}
	if err := json.Unmarshal(bodyBytes, &bodyMap); err != nil {
		return nil, fmt.Errorf("failed to parse request body: %w", err)
	}

	return &BodyContext{
		BodyBytes: bodyBytes,
		BodyMap:   bodyMap,
	}, nil
}

// RestoreBody restores the request body for downstream handlers
func RestoreBody(c *gin.Context, bodyBytes []byte) {
	c.Request.Body = io.NopCloser(bytes.NewReader(bodyBytes))
	c.Request.ContentLength = int64(len(bodyBytes))
	c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(bodyBytes)))
}

// IsSoftDeleteRequest checks if the request is a soft delete operation
// A soft delete is identified by the presence of a non-empty deletion_timestamp field in metadata
func IsSoftDeleteRequest(requestBody map[string]interface{}) bool {
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

// ExtractFilterValue extracts value from PostgREST filter format (e.g., "eq.value" -> "value")
func ExtractFilterValue(filter string) string {
	if filter == "" {
		return ""
	}

	// PostgREST format: operator.value (e.g., "eq.my-workspace")
	// Find the first dot and take everything after it
	for i := 0; i < len(filter); i++ {
		if filter[i] == '.' {
			if i+1 < len(filter) {
				return filter[i+1:]
			}

			return ""
		}
	}

	// If no dot found, return the whole string (shouldn't happen with PostgREST)
	return filter
}

// ExtractResourceIdentifiers extracts workspace and name from PostgREST query parameters
// Returns (workspace, name, error)
// - For user_profile: returns ("", id, nil) where id is the user ID
// - For workspace: returns ("", name, nil) where name is the workspace name
// - For other resources: returns (workspace, name, nil)
// tableName should be one of the storage table constants (e.g., storage.WORKSPACE_TABLE)
func ExtractResourceIdentifiers(queryParams url.Values, tableName string) (workspace, name string, err error) {
	// For user_profile, we use id instead of name
	if tableName == storage.USER_PROFILE_TABLE {
		id := ExtractFilterValue(queryParams.Get("id"))
		if id == "" {
			return "", "", fmt.Errorf("user_profile id not found in query params")
		}

		return "", id, nil
	}

	// For workspace, we only need name (no workspace field)
	if tableName == storage.WORKSPACE_TABLE {
		name = ExtractFilterValue(queryParams.Get("metadata->>name"))
		if name == "" {
			return "", "", fmt.Errorf("workspace name not found in query params")
		}

		return "", name, nil
	}

	// For other resources, we need both workspace and name
	workspace = ExtractFilterValue(queryParams.Get("metadata->>workspace"))
	name = ExtractFilterValue(queryParams.Get("metadata->>name"))

	if workspace == "" || name == "" {
		return "", "", fmt.Errorf("workspace or name not found in query params")
	}

	return workspace, name, nil
}
