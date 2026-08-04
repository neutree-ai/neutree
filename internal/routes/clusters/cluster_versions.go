package clusters

import (
	"fmt"
	"net/http"
	"sort"

	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type availableClusterVersionsResponse struct {
	AvailableVersions []string `json:"available_versions"`
}

type versionMatrixResponse struct {
	Versions []versionMatrixVersion `json:"versions"`
}

type versionMatrixVersion struct {
	Version   string                            `json:"version"`
	State     v1.ReleaseInfoClusterVersionState `json:"state"`
	UpgradeTo []string                          `json:"upgrade_to"`
}

type clusterUpgradePreflightResponse struct {
	Allowed       bool                    `json:"allowed"`
	SourceVersion string                  `json:"source_version"`
	TargetVersion string                  `json:"target_version,omitempty"`
	UpgradeTo     []string                `json:"upgrade_to"`
	ReleaseInfo   v1.ReleaseInfoReference `json:"release_info"`
}

// getVersionMatrix returns only user-selectable ReleaseInfo summary data.
func getVersionMatrix(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Query("workspace") == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace is required"})
			return
		}

		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		versions := make([]versionMatrixVersion, 0, len(info.Spec.ClusterVersions))

		for _, version := range info.Spec.ClusterVersions {
			upgradeTo := make([]string, len(version.UpgradeTo))
			copy(upgradeTo, version.UpgradeTo)
			versions = append(versions, versionMatrixVersion{
				Version:   version.Version,
				State:     version.State,
				UpgradeTo: upgradeTo,
			})
		}

		c.JSON(http.StatusOK, versionMatrixResponse{Versions: versions})
	}
}

// getAvailableClusterVersions handles GET /clusters/available_versions.
// It preserves the established response shape while ReleaseInfo becomes the
// only version authority. Registry access only controls material availability.
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

		if err := validateClusterType(c.Query("cluster_type")); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		imageRegistry, err := getImageRegistry(deps.Storage, workspace, imageRegistryName)
		if err != nil {
			status := http.StatusInternalServerError
			if err == errImageRegistryNotFound {
				status = http.StatusNotFound
			}

			c.JSON(status, gin.H{"error": imageRegistryErrorMessage(err, workspace, imageRegistryName)})

			return
		}

		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image prefix: %v", err)})
			return
		}

		auth, err := imageRegistryAuthenticator(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get auth info: %v", err)})
			return
		}

		available := make([]string, 0, len(info.Spec.ClusterVersions))

		for _, version := range info.Spec.ClusterVersions {
			if version.State != v1.ReleaseInfoClusterVersionStateActive {
				continue
			}

			availableForVersion, checkErr := checkReleaseImages(
				deps.ImageService,
				imagePrefix,
				auth,
				util.IsHTTPRegistryURL(imageRegistry.Spec.URL),
				version,
				c.Query("cluster_type"),
				c.Query("accelerator_type"),
			)
			if checkErr != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to check release images: %v", checkErr)})
				return
			}

			if availableForVersion {
				available = append(available, version.Version)
			}
		}

		c.JSON(http.StatusOK, availableClusterVersionsResponse{AvailableVersions: available})
	}
}

// getClusterUpgradePreflight validates a requested Cluster upgrade against the
// current ReleaseInfo edge. It intentionally does not recheck registry images:
// material availability is discovery-only, while this endpoint checks matrix
// compatibility for the CLI and UI before a PATCH.
func getClusterUpgradePreflight(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Query("workspace")
		name := c.Query("name")

		if workspace == "" || name == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace and name are required"})
			return
		}

		cluster, err := findCluster(deps.Storage, workspace, name)
		if err != nil {
			status := http.StatusInternalServerError
			if err == storage.ErrResourceNotFound {
				status = http.StatusNotFound
			}

			c.JSON(status, gin.H{"error": fmt.Sprintf("failed to get cluster: %v", err)})

			return
		}

		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		if info.Metadata == nil || info.Status == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "release info metadata and status are required"})
			return
		}

		sourceVersion := effectiveClusterVersion(cluster)

		source := releaseInfoVersion(info, sourceVersion)
		if source == nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("cluster version %s is not supported by the current release info", sourceVersion)})
			return
		}

		if source.State != v1.ReleaseInfoClusterVersionStateActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("cluster version %s is not active", sourceVersion)})
			return
		}

		response := clusterUpgradePreflightResponse{
			Allowed:       true,
			SourceVersion: sourceVersion,
			UpgradeTo:     append([]string(nil), source.UpgradeTo...),
			ReleaseInfo: v1.ReleaseInfoReference{
				Baseline: info.Metadata.Name,
				Revision: info.Status.Revision,
			},
		}

		targetVersion := c.Query("target_version")
		if targetVersion == "" {
			c.JSON(http.StatusOK, response)
			return
		}

		target := releaseInfoVersion(info, targetVersion)
		if target == nil || target.State != v1.ReleaseInfoClusterVersionStateActive {
			c.JSON(http.StatusBadRequest, gin.H{"error": fmt.Sprintf("target cluster version %s is not active", targetVersion)})
			return
		}

		if !releaseInfoUpgradeAllowed(source, targetVersion) {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": fmt.Sprintf(
					"cluster version update from %s to %s is not allowed by the current release info",
					sourceVersion,
					targetVersion,
				),
			})

			return
		}

		response.TargetVersion = targetVersion
		c.JSON(http.StatusOK, response)
	}
}

func currentReleaseInfo(deps *Dependencies) (*v1.ReleaseInfo, error) {
	if deps == nil || deps.ReleaseInfoProvider == nil {
		return nil, fmt.Errorf("release info provider is required")
	}

	info, err := deps.ReleaseInfoProvider.Current()
	if err != nil {
		return nil, err
	}

	if info == nil || info.Spec == nil {
		return nil, fmt.Errorf("release info spec is required")
	}

	return info, nil
}

func findCluster(s storage.Storage, workspace, name string) (*v1.Cluster, error) {
	clusters, err := s.ListCluster(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, name)},
		{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
	}})
	if err != nil {
		return nil, err
	}

	if len(clusters) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	return &clusters[0], nil
}

func effectiveClusterVersion(cluster *v1.Cluster) string {
	if cluster != nil && cluster.Status != nil && cluster.Status.Version != "" {
		return cluster.Status.Version
	}

	if cluster == nil || cluster.Spec == nil {
		return ""
	}

	return cluster.Spec.Version
}

func releaseInfoVersion(info *v1.ReleaseInfo, want string) *v1.ReleaseInfoClusterVersion {
	if info == nil || info.Spec == nil {
		return nil
	}

	for index := range info.Spec.ClusterVersions {
		if info.Spec.ClusterVersions[index].Version == want {
			return &info.Spec.ClusterVersions[index]
		}
	}

	return nil
}

func releaseInfoUpgradeAllowed(source *v1.ReleaseInfoClusterVersion, target string) bool {
	if source == nil {
		return false
	}

	for _, allowed := range source.UpgradeTo {
		if allowed == target {
			return true
		}
	}

	return false
}

func validateClusterType(clusterType string) error {
	switch clusterType {
	case string(v1.SSHClusterType), string(v1.KubernetesClusterType):
		return nil
	case "":
		return fmt.Errorf("cluster_type is required")
	default:
		return fmt.Errorf("unsupported cluster_type: %s, must be 'ssh' or 'kubernetes'", clusterType)
	}
}

var errImageRegistryNotFound = fmt.Errorf("image registry not found")

func getImageRegistry(s storage.Storage, workspace, name string) (*v1.ImageRegistry, error) {
	registries, err := s.ListImageRegistry(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, name)},
		{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
	}})
	if err != nil {
		return nil, err
	}

	if len(registries) == 0 {
		return nil, errImageRegistryNotFound
	}

	return &registries[0], nil
}

func imageRegistryErrorMessage(err error, workspace, name string) string {
	if err == errImageRegistryNotFound {
		return fmt.Sprintf("image registry %s/%s not found", workspace, name)
	}

	return fmt.Sprintf("failed to get image registry: %v", err)
}

func imageRegistryAuthenticator(imageRegistry *v1.ImageRegistry) (authn.Authenticator, error) {
	username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, err
	}

	if username == "" && password == "" {
		return authn.Anonymous, nil
	}

	return authn.FromConfig(authn.AuthConfig{Username: username, Password: password}), nil
}

func checkReleaseImages(
	imageService interface {
		CheckImageExists(string, authn.Authenticator, bool) (bool, error)
	},
	imagePrefix string,
	auth authn.Authenticator,
	useHTTP bool,
	version v1.ReleaseInfoClusterVersion,
	clusterType string,
	acceleratorType string,
) (bool, error) {
	images := requiredReleaseImages(version, clusterType, acceleratorType)
	for _, image := range images {
		rewritten := util.RewriteImageRef(imagePrefix, image)
		exists, err := imageService.CheckImageExists(rewritten, auth, useHTTP)

		if err != nil {
			return false, fmt.Errorf("check image %s: %w", rewritten, err)
		}

		if !exists {
			return false, nil
		}
	}

	return true, nil
}

func requiredReleaseImages(version v1.ReleaseInfoClusterVersion, clusterType, acceleratorType string) []string {
	requiredComponents := map[string]struct{}{
		"ray_runtime":   {},
		"node_agent":    {},
		"node_exporter": {},
		"vmagent":       {},
	}
	if clusterType == string(v1.KubernetesClusterType) {
		requiredComponents["router"] = struct{}{}
		requiredComponents["kube_state_metrics"] = struct{}{}
	}

	unique := make(map[string]struct{}, len(requiredComponents)+len(version.AcceleratorComponents[acceleratorType]))

	for component := range requiredComponents {
		if image := version.Components[component]; image != "" {
			unique[image] = struct{}{}
		}
	}

	for _, image := range version.AcceleratorComponents[acceleratorType] {
		unique[image] = struct{}{}
	}

	images := make([]string, 0, len(unique))
	for image := range unique {
		images = append(images, image)
	}

	sort.Strings(images)

	return images
}
