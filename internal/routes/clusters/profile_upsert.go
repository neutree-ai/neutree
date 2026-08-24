package clusters

import (
	"encoding/json"
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type profileUpsertRequest struct {
	Profile     *v1.ClusterProfile `json:"profile"`
	ForceUpdate json.RawMessage    `json:"force_update"`
}

type profileUpsertResponse struct {
	Operation string `json:"operation"`
}

func upsertClusterProfile(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var request profileUpsertRequest
		if err := c.ShouldBindJSON(&request); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid cluster profile payload: %v", err)})
			return
		}

		if request.ForceUpdate != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "force_update is not supported for cluster profiles"})
			return
		}

		if err := validateProfileForUpsert(request.Profile); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		if err := profileEligibleForReleaseInfo(request.Profile.GetName(), info); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list cluster profiles: %v", err)})
			return
		}

		for index := range profiles {
			if profiles[index].GetName() != request.Profile.GetName() {
				continue
			}

			if sameClusterProfileContent(&profiles[index], request.Profile) {
				c.JSON(http.StatusOK, profileUpsertResponse{Operation: "unchanged"})
				return
			}

			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf(
				"cluster profile %s already exists with different content",
				request.Profile.GetName(),
			)})

			return
		}

		if err := deps.Storage.CreateClusterProfile(request.Profile); err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to create cluster profile: %v", err)})
			return
		}

		c.JSON(http.StatusOK, profileUpsertResponse{Operation: "created"})
	}
}

func validateProfileForUpsert(profile *v1.ClusterProfile) error {
	if profile == nil {
		return fmt.Errorf("profile is required")
	}

	if profile.APIVersion != "v1" {
		return fmt.Errorf("profile api_version must be v1")
	}

	if profile.Kind != v1.ClusterProfileKind {
		return fmt.Errorf("profile kind must be %s", v1.ClusterProfileKind)
	}

	if profile.Metadata == nil || strings.TrimSpace(profile.Metadata.Name) == "" {
		return fmt.Errorf("profile metadata.name is required")
	}

	if profile.Metadata.Workspace != "" {
		return fmt.Errorf("profile metadata.workspace is not supported")
	}

	if _, err := parseExactClusterVersion(profile.Metadata.Name); err != nil {
		return err
	}

	if profile.Spec == nil {
		return fmt.Errorf("profile spec is required")
	}

	if len(profile.Spec.Components) != 2 {
		return fmt.Errorf("profile spec.components must contain exactly %q and %q", v1.SSHClusterType, v1.KubernetesClusterType)
	}

	for clusterType := range profile.Spec.Components {
		if !v1.IsSupportedClusterType(clusterType) {
			return fmt.Errorf("profile spec.components contains unsupported cluster type %q", clusterType)
		}
	}

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		components, found := profile.Spec.ComponentsFor(clusterType)
		if !found {
			return fmt.Errorf("profile spec.components.%s is required", clusterType)
		}

		for _, component := range requiredProfileComponents(clusterType, components) {
			if strings.TrimSpace(component.ref.Image) == "" {
				return fmt.Errorf("profile spec.components.%s.image is required", component.name)
			}

			if strings.TrimSpace(component.ref.Tag) == "" {
				return fmt.Errorf("profile spec.components.%s.tag is required", component.name)
			}
		}
	}

	return nil
}

func profileEligibleForReleaseInfo(version string, info *v1.ReleaseInfo) error {
	if info == nil || info.Spec == nil {
		return fmt.Errorf("release info metadata and spec are required")
	}

	profileVersion, err := parseExactClusterVersion(version)
	if err != nil {
		return err
	}

	defaultVersion, err := parseExactClusterVersion(info.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	if profileVersion.GreaterThan(defaultVersion) {
		return fmt.Errorf("cluster profile %s exceeds default cluster version %s", version, info.Spec.DefaultClusterVersion)
	}

	minor, err := releaseprofile.NormalizeClusterMinor(version)
	if err != nil {
		return err
	}

	for _, compatible := range info.Spec.CompatibleClusterBaselines {
		if compatible == minor {
			return nil
		}
	}

	return fmt.Errorf("cluster profile %s is not compatible with current release info", version)
}

type profileComponent struct {
	name string
	ref  v1.ImageRef
}

func requiredProfileComponents(clusterType string, components v1.ClusterProfileComponents) []profileComponent {
	switch clusterType {
	case v1.SSHClusterType:
		return []profileComponent{
			{name: "ray_runtime", ref: components.RayRuntime},
			{name: "node_agent", ref: components.NodeAgent},
			{name: "node_exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
		}
	case v1.KubernetesClusterType:
		return []profileComponent{
			{name: "kubernetes_runtime", ref: components.KubernetesRuntime},
			{name: "router", ref: components.Router},
			{name: "node_agent", ref: components.NodeAgent},
			{name: "node_exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
			{name: "kube_state_metrics", ref: components.KubeStateMetrics},
		}
	default:
		return nil
	}
}

func sameClusterProfileContent(existing, requested *v1.ClusterProfile) bool {
	if existing == nil || requested == nil || existing.Spec == nil || requested.Spec == nil || existing.Metadata == nil || requested.Metadata == nil {
		return false
	}

	return existing.APIVersion == requested.APIVersion &&
		existing.Kind == requested.Kind &&
		existing.GetName() == requested.GetName() &&
		existing.Metadata.Workspace == requested.Metadata.Workspace &&
		maps.Equal(existing.Metadata.Labels, requested.Metadata.Labels) &&
		maps.Equal(existing.Metadata.Annotations, requested.Metadata.Annotations) &&
		reflect.DeepEqual(existing.Spec, requested.Spec)
}

func parseExactClusterVersion(version string) (*semver.Version, error) {
	if strings.TrimSpace(version) != version || !strings.HasPrefix(version, "v") {
		return nil, fmt.Errorf("cluster version %q must use v-prefixed semantic version", version)
	}

	parsed, err := semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
	if err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", version, err)
	}

	return parsed, nil
}
