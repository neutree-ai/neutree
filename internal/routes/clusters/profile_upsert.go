package clusters

import (
	"fmt"
	"maps"
	"net/http"
	"reflect"
	"strings"

	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const uniqueViolationCode = "23505"

type profileUpsertResponse struct {
	Operation string `json:"operation"`
}

// upsertClusterProfile checks the Profile version against current policy, then
// creates absent versions or permits exact replay. It never updates a
// stored Profile because component drift would change deployed runtime behavior.
func upsertClusterProfile(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		profile, found := middleware.ClusterProfileImportFromContext(c)
		if !found {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})

			return
		}

		if deps.ReleaseInfoProvider == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})

			return
		}

		info, err := deps.ReleaseInfoProvider.Current()
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})

			return
		}

		if err := releaseprofile.ValidateClusterVersionCompatibility(info, profile.GetName()); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})

			return
		}

		existing, err := findClusterProfile(deps.Storage, profile.GetName())
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})

			return
		}

		if existing != nil {
			respondExistingClusterProfile(c, existing, profile)
			return
		}

		if err := deps.Storage.CreateClusterProfile(profile); err != nil {
			if isUniqueViolation(err) {
				existing, reloadErr := findClusterProfile(deps.Storage, profile.GetName())
				if reloadErr == nil && existing != nil {
					respondExistingClusterProfile(c, existing, profile)

					return
				}
			}

			c.JSON(http.StatusInternalServerError, gin.H{"error": "internal server error"})

			return
		}

		c.JSON(http.StatusOK, profileUpsertResponse{Operation: "created"})
	}
}

func findClusterProfile(s storage.Storage, version string) (*v1.ClusterProfile, error) {
	profiles, err := s.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	for index := range profiles {
		if profiles[index].GetName() == version {
			return &profiles[index], nil
		}
	}

	return nil, nil
}

func respondExistingClusterProfile(c *gin.Context, existing, incoming *v1.ClusterProfile) {
	if clusterProfilesSemanticallyEqual(existing, incoming) {
		c.JSON(http.StatusOK, profileUpsertResponse{Operation: "unchanged"})

		return
	}

	c.JSON(http.StatusConflict, gin.H{
		"error": fmt.Sprintf("cluster profile %s already exists with different content", incoming.GetName()),
	})
}

func clusterProfilesSemanticallyEqual(left, right *v1.ClusterProfile) bool {
	if left == nil || right == nil || left.Metadata == nil || right.Metadata == nil {
		return left == right
	}

	return left.APIVersion == right.APIVersion &&
		left.Kind == right.Kind &&
		left.Metadata.Name == right.Metadata.Name &&
		left.Metadata.Workspace == right.Metadata.Workspace &&
		maps.Equal(left.Metadata.Labels, right.Metadata.Labels) &&
		maps.Equal(left.Metadata.Annotations, right.Metadata.Annotations) &&
		reflect.DeepEqual(left.Spec, right.Spec)
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), uniqueViolationCode)
}
