package proxies

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"
	"k8s.io/klog/v2"

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
	proxyGroup.GET("/available_versions", getAvailableClusterVersions(deps))
}

// newImageService is a factory function for creating image services.
// It can be overridden in tests to inject mocks.
var newImageService = registry.NewImageService

type availableClusterVersionsResponse struct {
	AvailableVersions []string `json:"available_versions"`
}

// getAvailableClusterVersions handles GET /clusters/available_versions
// Query params: image_registry (required), workspace (required), cluster_type (required), accelerator_type (optional)
func getAvailableClusterVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Query("workspace")
		if workspace == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace is required"})
			return
		}

		imageRegistryName := c.Query("image_registry")
		if imageRegistryName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "image_registry is required"})
			return
		}

		clusterType := c.Query("cluster_type")
		if clusterType == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "cluster_type is required"})
			return
		}

		acceleratorType := c.Query("accelerator_type")

		// Get image registry
		imageRegistries, err := deps.Storage.ListImageRegistry(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, imageRegistryName)},
				{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
			},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image registry: %v", err)})
			return
		}

		if len(imageRegistries) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("image registry %s/%s not found", workspace, imageRegistryName)})
			return
		}

		imageRegistry := &imageRegistries[0]

		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image prefix: %v", err)})
			return
		}

		var imageName string

		switch clusterType {
		case string(v1.SSHClusterType):
			imageName = v1.NeutreeServeImageName
		case string(v1.KubernetesClusterType):
			imageName = v1.NeutreeRouterImageName
		default:
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("unsupported cluster_type: %s, must be 'ssh' or 'kubernetes'", clusterType)})
			return
		}

		imageRepo := imagePrefix + "/" + imageName

		// Build auth
		username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get auth info: %v", err)})
			return
		}

		var auth authn.Authenticator
		if username != "" || password != "" {
			auth = authn.FromConfig(authn.AuthConfig{
				Username: username,
				Password: password,
			})
		} else {
			auth = authn.Anonymous
		}

		// List tags, then read image labels to discover available versions.
		// Only images with the "neutree.ai/cluster-version" label are included;
		// images without the label (dev/nightly builds) are skipped.
		// Labeled images are deduplicated by version and filtered by accelerator type.
		imageSvc := newImageService()

		tags, err := imageSvc.ListImageTags(imageRepo, auth)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list image tags: %v", err)})
			return
		}

		seen := make(map[string]struct{})
		var versions []*semver.Version

		for _, tag := range tags {
			imageRef := imageRepo + ":" + tag

			labels, err := imageSvc.GetImageLabels(imageRef, auth)
			if err != nil {
				klog.V(4).Infof("skipping tag %s: failed to get labels: %v", tag, err)
				continue
			}

			versionStr := labels[v1.ImageLabelVersion]
			if versionStr == "" {
				// No version label — skip
				continue
			}

			// Filter by accelerator type if specified
			if acceleratorType != "" {
				imageAcceleratorType := labels[v1.ImageLabelAcceleratorType]
				if imageAcceleratorType != acceleratorType {
					continue
				}
			}

			v, err := semver.NewVersion(versionStr)
			if err != nil {
				continue
			}

			key := v.String()
			if _, ok := seen[key]; ok {
				continue
			}

			seen[key] = struct{}{}

			versions = append(versions, v)
		}

		sort.Sort(semver.Collection(versions))

		availableVersions := make([]string, 0, len(versions))
		for _, v := range versions {
			availableVersions = append(availableVersions, "v"+v.String())
		}

		c.JSON(http.StatusOK, availableClusterVersionsResponse{
			AvailableVersions: availableVersions,
		})
	}
}
