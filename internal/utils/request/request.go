package request

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/gin-gonic/gin"
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

// ExtractResourceIdentifiers extracts workspace and name from request body metadata
// Returns (workspace, name, error)
// workspace may be empty for non-workspaced resources
func ExtractResourceIdentifiers(bodyMap map[string]interface{}) (workspace, name string, err error) {
	// Extract metadata from body
	metadata, ok := bodyMap["metadata"].(map[string]interface{})
	if !ok {
		return "", "", fmt.Errorf("metadata not found in request body")
	}

	// Extract name (required)
	name, ok = metadata["name"].(string)
	if !ok || name == "" {
		return "", "", fmt.Errorf("name not found in metadata")
	}

	// Extract workspace (optional, may be empty for non-workspaced resources)
	workspace, _ = metadata["workspace"].(string)

	return workspace, name, nil
}
