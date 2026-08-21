package clusters

import (
	"encoding/json"
	"fmt"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
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

		// RawMessage preserves an explicit JSON null, which must be rejected just
		// like true or false so callers cannot retain a force-update escape hatch.
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

		minor, err := releaseinfo.NormalizeClusterMinor(request.Profile.GetName())
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		if !compatibleClusterBaselines(info.Spec.CompatibleClusterBaselines)[minor] {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("cluster profile %s is not compatible with current release info", request.Profile.GetName())})
			return
		}

		profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list cluster profiles: %v", err)})
			return
		}

		for index := range profiles {
			if profiles[index].GetName() != request.Profile.GetName() ||
				profiles[index].GetClusterType() != request.Profile.GetClusterType() {
				continue
			}

			if sameClusterProfileContent(&profiles[index], request.Profile) {
				c.JSON(http.StatusOK, profileUpsertResponse{Operation: "unchanged"})
				return
			}

			c.JSON(http.StatusConflict, gin.H{"error": fmt.Sprintf(
				"cluster profile %s/%s already exists with different content",
				request.Profile.GetName(), request.Profile.GetClusterType(),
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

	if profile.Spec == nil {
		return fmt.Errorf("profile spec is required")
	}

	if !v1.IsSupportedClusterType(profile.Spec.ClusterType) {
		return fmt.Errorf("profile spec.cluster_type must be %q or %q", v1.SSHClusterType, v1.KubernetesClusterType)
	}

	if _, err := releaseinfo.NormalizeClusterMinor(profile.Metadata.Name); err != nil {
		return err
	}

	for _, component := range requiredProfileComponents(profile.Spec.ClusterType, profile.Spec.Components) {
		if strings.TrimSpace(component.ref.Image) == "" {
			return fmt.Errorf("profile spec.components.%s.image is required", component.name)
		}

		if strings.TrimSpace(component.ref.Tag) == "" {
			return fmt.Errorf("profile spec.components.%s.tag is required", component.name)
		}
	}

	return nil
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
	if existing == nil || requested == nil || existing.Spec == nil || requested.Spec == nil {
		return false
	}

	return existing.APIVersion == requested.APIVersion &&
		existing.Kind == requested.Kind &&
		existing.GetName() == requested.GetName() &&
		existing.GetClusterType() == requested.GetClusterType() &&
		reflect.DeepEqual(existing.Spec.Components, requested.Spec.Components)
}
