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

// StampCredentialOwnerLabel stamps newly created resources with the current
// user as credential owner. Only acts on POST; other methods pass through.
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

		if err := rewriteLabel(c, func(labels map[string]interface{}) {
			labels[v1.LabelCredentialOwner] = userID
		}); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()

			return
		}

		c.Next()
	}
}

// PinCredentialOwnerLabel makes the credential-owner label immutable on PATCH:
// whatever the client sends for it is discarded and replaced with the value
// already stored for the targeted resource, so an update can neither reassign
// nor strip ownership of a credential.
func PinCredentialOwnerLabel(deps *Dependencies, tableName string) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch {
			c.Next()
			return
		}

		var current []struct {
			Metadata struct {
				Labels map[string]string `json:"labels"`
			} `json:"metadata"`
		}

		if err := deps.Storage.GenericQuery(tableName, "metadata", queryParamsToFilters(c.Request.URL.Query()), &current); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("failed to look up current resource: %v", err)})
			c.Abort()

			return
		}

		if len(current) == 0 {
			// Nothing to protect: let the request through and let PostgREST
			// report the not-found/permission-denied case.
			c.Next()
			return
		}

		existingOwner := current[0].Metadata.Labels[v1.LabelCredentialOwner]

		if err := rewriteLabel(c, func(labels map[string]interface{}) {
			if existingOwner == "" {
				delete(labels, v1.LabelCredentialOwner)
			} else {
				labels[v1.LabelCredentialOwner] = existingOwner
			}
		}); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			c.Abort()

			return
		}

		c.Next()
	}
}

// rewriteLabel reads the request JSON body, applies mutate to metadata.labels
// (creating metadata/labels objects as needed), and re-serializes the body.
// A missing or empty body is left untouched.
func rewriteLabel(c *gin.Context, mutate func(labels map[string]interface{})) error {
	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return fmt.Errorf("failed to read request body: %w", err)
	}

	_ = c.Request.Body.Close()

	if len(bytes.TrimSpace(body)) == 0 {
		c.Request.Body = io.NopCloser(bytes.NewReader(body))
		c.Request.ContentLength = int64(len(body))

		return nil
	}

	var payload map[string]interface{}
	if err := json.Unmarshal(body, &payload); err != nil {
		return fmt.Errorf("failed to parse request body: %w", err)
	}

	metadata, err := objectField(payload, "metadata")
	if err != nil {
		return err
	}

	labels, err := objectField(metadata, "labels")
	if err != nil {
		return err
	}

	mutate(labels)

	modifiedBody, err := json.Marshal(payload)
	if err != nil {
		return fmt.Errorf("failed to marshal request body: %w", err)
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(modifiedBody))
	c.Request.ContentLength = int64(len(modifiedBody))
	c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBody)))

	return nil
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
