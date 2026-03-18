package proxies

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

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

		// Collect all valid semver tags, dedup after stripping accelerator suffixes.
		// Accelerator variants like "v1.0.0-rocm" are merged into "v1.0.0",
		// but release candidates like "v1.0.1-rc.1" are kept as distinct versions.
		seen := make(map[string]struct{})
		var versions []*semver.Version

		for _, tag := range tags {
			v, err := semver.NewVersion(tag)
			if err != nil {
				continue
			}

			normalized := stripAcceleratorSuffix(v)
			key := normalized.String()
			if _, ok := seen[key]; ok {
				continue
			}

			seen[key] = struct{}{}
			versions = append(versions, normalized)
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

// stripAcceleratorSuffix extracts the logical version by removing the accelerator
// suffix from the semver prerelease, based on the tag naming convention:
//
//	v1.0.0                → v1.0.0           (no prerelease)
//	v1.0.0-rocm           → v1.0.0           (prerelease "rocm" is purely alphabetic → accelerator suffix)
//	v1.0.1-rc.1           → v1.0.1-rc.1      (last segment "rc.1" is not purely alphabetic → kept)
//	v1.0.1-rc.1-rocm      → v1.0.1-rc.1      (last segment "rocm" is purely alphabetic → stripped)
//
// Convention: the accelerator suffix is appended with "-" (e.g. "v1.0.0-rocm").
// The prerelease is split by "-" and the last segment is treated as an accelerator
// suffix if it is purely alphabetic. Semantic prerelease segments always contain
// non-alpha characters (e.g. "rc.1", "alpha.2", "beta.3").
func stripAcceleratorSuffix(v *semver.Version) *semver.Version {
	pre := v.Prerelease()
	if pre == "" {
		return v
	}

	segments := strings.Split(pre, "-")
	last := segments[len(segments)-1]

	if !isAlpha(last) {
		return v
	}

	base := fmt.Sprintf("%d.%d.%d", v.Major(), v.Minor(), v.Patch())
	if len(segments) > 1 {
		base += "-" + strings.Join(segments[:len(segments)-1], "-")
	}

	return semver.MustParse(base)
}

// isAlpha returns true if s is non-empty and contains only ASCII letters.
func isAlpha(s string) bool {
	if s == "" {
		return false
	}

	for _, c := range s {
		if (c < 'a' || c > 'z') && (c < 'A' || c > 'Z') {
			return false
		}
	}

	return true
}
