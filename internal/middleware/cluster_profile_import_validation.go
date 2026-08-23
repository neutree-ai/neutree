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
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

const clusterProfileImportContextKey = "cluster-profile-import"

// ReleaseInfoProvider resolves the policy that gates cluster package imports.
type ReleaseInfoProvider interface {
	Current() (*v1.ReleaseInfo, error)
}

// ClusterProfileImportValidation rejects invalid package Profile requests before
// they reach storage. Semantic checks stay in releaseprofile instead of SQL.
func ClusterProfileImportValidation(provider ReleaseInfoProvider) gin.HandlerFunc {
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

		if provider == nil {
			writeClusterProfileImportError(c, http.StatusInternalServerError, "internal server error")

			return
		}

		info, err := provider.Current()
		if err != nil {
			writeClusterProfileImportError(c, http.StatusInternalServerError, "internal server error")

			return
		}

		if err := releaseprofile.ValidateProfileEligibility(info, profile); err != nil {
			writeClusterProfileImportError(c, http.StatusBadRequest, err.Error())

			return
		}

		c.Set(clusterProfileImportContextKey, profile)
		c.Next()
	}
}

// ClusterProfileImportFromContext returns the Profile validated by the import
// middleware. Handlers must not re-decode or re-validate the request payload.
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
