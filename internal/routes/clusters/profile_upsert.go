package clusters

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type profileUpsertRequest struct {
	Profile     *v1.ClusterProfile `json:"profile"`
	ForceUpdate bool               `json:"force_update"`
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
			if profiles[index].GetName() != request.Profile.GetName() {
				continue
			}
			if !request.ForceUpdate {
				c.JSON(http.StatusOK, profileUpsertResponse{Operation: "unchanged"})
				return
			}
			if err := deps.Storage.UpdateClusterProfile(profiles[index].GetID(), request.Profile); err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to update cluster profile: %v", err)})
				return
			}
			c.JSON(http.StatusOK, profileUpsertResponse{Operation: "updated"})
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
	if _, err := releaseinfo.NormalizeClusterMinor(profile.Metadata.Name); err != nil {
		return err
	}

	components := []struct {
		name string
		ref  v1.ImageRef
	}{
		{name: "ray_runtime", ref: profile.Spec.Components.RayRuntime},
		{name: "router", ref: profile.Spec.Components.Router},
		{name: "node_agent", ref: profile.Spec.Components.NodeAgent},
		{name: "node_exporter", ref: profile.Spec.Components.NodeExporter},
		{name: "vmagent", ref: profile.Spec.Components.VMAgent},
		{name: "kube_state_metrics", ref: profile.Spec.Components.KubeStateMetrics},
	}
	for _, component := range components {
		if strings.TrimSpace(component.ref.Image) == "" {
			return fmt.Errorf("profile spec.components.%s.image is required", component.name)
		}
		if strings.TrimSpace(component.ref.Tag) == "" {
			return fmt.Errorf("profile spec.components.%s.tag is required", component.name)
		}
	}

	return nil
}
