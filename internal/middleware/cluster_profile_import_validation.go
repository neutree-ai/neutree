package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const clusterProfileImportContextKey = "cluster-profile-import"

// ClusterProfileImportValidation rejects malformed package Profile requests
// before they reach the API handler. Domain eligibility stays with the handler
// and releaseprofile instead of SQL.
func ClusterProfileImportValidation() gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			c.Next()

			return
		}

		profile, err := readClusterProfileImport(c)
		if err != nil {
			writeClusterProfileImportError(c, http.StatusBadRequest, err.Error())

			return
		}

		c.Set(clusterProfileImportContextKey, profile)
		c.Next()
	}
}

// ClusterProfileImportFromContext returns the Profile whose request shape was
// validated by the import middleware. Handlers own domain validation.
func ClusterProfileImportFromContext(c *gin.Context) (*v1.ClusterProfile, bool) {
	value, found := c.Get(clusterProfileImportContextKey)
	if !found {
		return nil, false
	}

	profile, ok := value.(*v1.ClusterProfile)

	return profile, ok
}

func readClusterProfileImport(c *gin.Context) (*v1.ClusterProfile, error) {
	if c.Request.Body == nil {
		return nil, errors.New("cluster profile payload is required")
	}

	body, err := io.ReadAll(c.Request.Body)
	if err != nil {
		return nil, errors.New("invalid cluster profile payload")
	}

	if err := c.Request.Body.Close(); err != nil {
		return nil, errors.New("invalid cluster profile payload")
	}

	c.Request.Body = io.NopCloser(bytes.NewReader(body))
	c.Request.ContentLength = int64(len(body))
	c.Request.Header.Set("Content-Length", strconv.Itoa(len(body)))

	var rawPayload map[string]json.RawMessage
	if err := json.Unmarshal(body, &rawPayload); err != nil {
		return nil, errors.New("invalid cluster profile payload")
	}

	if _, found := rawPayload["force_update"]; found {
		return nil, errors.New("force_update is not supported for cluster profiles")
	}

	var request struct {
		Profile *v1.ClusterProfile `json:"profile"`
	}
	if err := json.Unmarshal(body, &request); err != nil {
		return nil, errors.New("invalid cluster profile payload")
	}

	if request.Profile == nil {
		return nil, errors.New("profile is required")
	}

	return request.Profile, nil
}

func writeClusterProfileImportError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
	c.Abort()
}
