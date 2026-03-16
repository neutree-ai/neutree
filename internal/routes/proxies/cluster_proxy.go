package proxies

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func validateClusterDeletion(s storage.Storage) middleware.DeletionValidatorFunc {
	return func(workspace, name string) error {
		count, err := s.Count(storage.ENDPOINT_TABLE, []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "spec->>cluster", Operator: "eq", Value: name},
		})
		if err != nil {
			return fmt.Errorf("failed to count endpoints: %w", err)
		}

		if count > 0 {
			return &middleware.DeletionError{
				Code:    "10126",
				Message: fmt.Sprintf("cannot delete cluster '%s/%s'", workspace, name),
				Hint:    fmt.Sprintf("%d endpoint(s) still reference this cluster", count),
			}
		}

		return nil
	}
}

func RegisterClusterRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/clusters")
	proxyGroup.Use(middlewares...)

	deletionValidation := middleware.DeletionValidation(
		storage.CLUSTERS_TABLE,
		validateClusterDeletion(deps.Storage),
	)
	handler := CreateStructProxyHandler[v1.Cluster](deps, storage.CLUSTERS_TABLE)

	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", deletionValidation, handler)
	proxyGroup.GET("/:workspace/:name/available_upgrade_versions", getAvailableUpgradeVersions(deps))
}

// newImageService is a factory function for creating image services.
// It can be overridden in tests to inject mocks.
var newImageService = registry.NewImageService

type availableUpgradeVersionsResponse struct {
	CurrentVersion    string   `json:"current_version"`
	AvailableVersions []string `json:"available_versions"`
}

func getAvailableUpgradeVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Param("workspace")
		name := c.Param("name")

		// Get cluster by workspace+name
		clusters, err := deps.Storage.ListCluster(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: name},
				{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get cluster: %v", err)})
			return
		}

		if len(clusters) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("cluster %s/%s not found", workspace, name)})
			return
		}

		cluster := clusters[0]

		if cluster.Spec == nil || cluster.Spec.Version == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster has no version specified"})
			return
		}

		currentVersion, err := semver.NewVersion(cluster.Spec.Version)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("invalid cluster version: %v", err)})
			return
		}

		// Get image registry
		imageRegistries, err := deps.Storage.ListImageRegistry(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, cluster.Spec.ImageRegistry)},
				{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
			},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image registry: %v", err)})
			return
		}

		if len(imageRegistries) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("image registry %s not found", cluster.Spec.ImageRegistry)})
			return
		}

		imageRegistry := &imageRegistries[0]

		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image prefix: %v", err)})
			return
		}

		// Build image repo based on cluster type
		var imageName string
		if cluster.Spec.Type == v1.SSHClusterType {
			imageName = v1.NeutreeServeImageName
		} else {
			imageName = v1.NeutreeRouterImageName
		}

		imageRepo := imagePrefix + "/" + imageName

		// Build auth
		username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get auth info: %v", err)})
			return
		}

		auth := authn.FromConfig(authn.AuthConfig{
			Username: username,
			Password: password,
		})

		// List tags
		imageSvc := newImageService()

		tags, err := imageSvc.ListImageTags(imageRepo, auth)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list image tags: %v", err)})
			return
		}

		// Filter and sort: keep only tags > current version
		var versions []*semver.Version

		for _, tag := range tags {
			v, err := semver.NewVersion(tag)
			if err != nil {
				continue
			}

			if v.GreaterThan(currentVersion) {
				versions = append(versions, v)
			}
		}

		sort.Sort(semver.Collection(versions))

		availableVersions := make([]string, 0, len(versions))
		for _, v := range versions {
			availableVersions = append(availableVersions, "v"+v.String())
		}

		c.JSON(http.StatusOK, availableUpgradeVersionsResponse{
			CurrentVersion:    cluster.Spec.Version,
			AvailableVersions: availableVersions,
		})
	}
}
