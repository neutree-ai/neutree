package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/Masterminds/semver/v3"
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
	if err := validateClusterProfileImport(request.Profile); err != nil {
		return nil, err
	}

	return request.Profile, nil
}

func validateClusterProfileImport(profile *v1.ClusterProfile) error {
	if profile.Metadata == nil || profile.Spec == nil {
		return errors.New("cluster profile metadata and spec are required")
	}
	if profile.APIVersion != "v1" {
		return errors.New("cluster profile api version must be v1")
	}
	if profile.Kind != v1.ClusterProfileKind {
		return fmt.Errorf("cluster profile kind must be %s", v1.ClusterProfileKind)
	}
	if profile.Metadata.Workspace != "" {
		return errors.New("cluster profile metadata.workspace must be empty")
	}
	if err := validateClusterProfileVersion(profile.Metadata.Name); err != nil {
		return err
	}

	for clusterType := range profile.Spec.Components {
		if !v1.IsSupportedClusterType(clusterType) {
			return fmt.Errorf("unsupported component matrix type %q", clusterType)
		}
	}

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		components, found := profile.Spec.ComponentsFor(clusterType)
		if !found {
			return fmt.Errorf("%s component matrix is required", clusterType)
		}

		for _, component := range requiredClusterProfileImages(clusterType, components) {
			if invalidClusterProfileImageValue(component.ref.Image) {
				return fmt.Errorf("%s image is required", component.name)
			}
			if invalidClusterProfileImageValue(component.ref.Tag) {
				return fmt.Errorf("%s tag is required", component.name)
			}
		}
	}

	return nil
}

func validateClusterProfileVersion(version string) error {
	if !strings.HasPrefix(version, "v") {
		return fmt.Errorf("invalid cluster profile version %q", version)
	}
	if _, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v")); err != nil {
		return fmt.Errorf("invalid cluster profile version %q: %w", version, err)
	}

	return nil
}

func invalidClusterProfileImageValue(value string) bool {
	return strings.TrimSpace(value) == "" || strings.TrimSpace(value) != value
}

type requiredClusterProfileImage struct {
	name string
	ref  v1.ImageRef
}

func requiredClusterProfileImages(
	clusterType string,
	components v1.ClusterProfileComponents,
) []requiredClusterProfileImage {
	switch clusterType {
	case v1.SSHClusterType:
		return []requiredClusterProfileImage{
			{name: "ray runtime", ref: components.RayRuntime},
			{name: "node agent", ref: components.NodeAgent},
			{name: "node exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
		}
	case v1.KubernetesClusterType:
		return []requiredClusterProfileImage{
			{name: "kubernetes runtime", ref: components.KubernetesRuntime},
			{name: "router", ref: components.Router},
			{name: "node agent", ref: components.NodeAgent},
			{name: "node exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
			{name: "kube state metrics", ref: components.KubeStateMetrics},
		}
	default:
		return nil
	}
}

func writeClusterProfileImportError(c *gin.Context, status int, message string) {
	c.JSON(status, gin.H{"error": message})
	c.Abort()
}
