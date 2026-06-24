package proxies

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const credentialOwnerFilterColumn = "metadata->labels->>" + v1.LabelCredentialOwner

func CredentialOwnerFilter(userID string) storage.Filter {
	return storage.Filter{
		Column:   credentialOwnerFilterColumn,
		Operator: "eq",
		Value:    userID,
	}
}

func AddCredentialOwnerQuery(query url.Values, userID string) url.Values {
	query.Set(credentialOwnerFilterColumn, "eq."+userID)
	return query
}

func StampCredentialOwnerLabel() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()
			return
		}

		userID := c.GetString("user_id")
		if userID == "" {
			c.JSON(http.StatusUnauthorized, gin.H{"error": "user not authenticated"})
			c.Abort()

			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to read request body: %v", err)})
			c.Abort()

			return
		}

		_ = c.Request.Body.Close()

		if len(bytes.TrimSpace(body)) == 0 {
			c.Request.Body = io.NopCloser(bytes.NewReader(body))
			c.Request.ContentLength = int64(len(body))
			c.Next()

			return
		}

		var payload map[string]interface{}
		if err := json.Unmarshal(body, &payload); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to parse request body: %v", err)})
			c.Abort()

			return
		}

		metadata, err := objectField(payload, "metadata")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()

			return
		}

		labels, err := objectField(metadata, "labels")
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()

			return
		}

		labels[v1.LabelCredentialOwner] = userID

		modifiedBody, err := json.Marshal(payload)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to marshal request body: %v", err)})
			c.Abort()

			return
		}

		c.Request.Body = io.NopCloser(bytes.NewReader(modifiedBody))
		c.Request.ContentLength = int64(len(modifiedBody))
		c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))

		c.Next()
	}
}

func objectField(parent map[string]interface{}, key string) (map[string]interface{}, error) {
	value, exists := parent[key]
	if !exists || value == nil {
		obj := make(map[string]interface{})
		parent[key] = obj

		return obj, nil
	}

	obj, ok := value.(map[string]interface{})
	if !ok {
		return nil, fmt.Errorf("%s must be an object", key)
	}

	return obj, nil
}
