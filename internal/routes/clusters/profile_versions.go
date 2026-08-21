package clusters

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// clusterProfileVersion exposes profile identity without disclosing its image
// material. It is used by the control-plane upgrade preflight.
type clusterProfileVersion struct {
	Version     string `json:"version"`
	ClusterType string `json:"cluster_type"`
}

type clusterProfileVersionsResponse struct {
	Profiles []clusterProfileVersion `json:"profiles"`
}

func getClusterProfileVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if deps == nil || deps.Storage == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage is required"})
			return
		}

		profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list cluster profiles: %v", err)})
			return
		}

		identities := make([]clusterProfileVersion, 0, len(profiles))

		for _, profile := range profiles {
			version := profile.GetName()
			clusterType := profile.GetClusterType()

			if version == "" || !v1.IsSupportedClusterType(clusterType) {
				continue
			}

			identities = append(identities, clusterProfileVersion{
				Version:     version,
				ClusterType: clusterType,
			})
		}

		sort.Slice(identities, func(i, j int) bool {
			if identities[i].Version == identities[j].Version {
				return identities[i].ClusterType < identities[j].ClusterType
			}

			return identities[i].Version < identities[j].Version
		})

		c.JSON(http.StatusOK, clusterProfileVersionsResponse{Profiles: identities})
	}
}
