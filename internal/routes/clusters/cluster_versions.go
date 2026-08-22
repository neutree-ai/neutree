package clusters

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"
	"github.com/google/go-containerregistry/pkg/authn"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	clusterVersionLabelKey    = v1.NeutreeServingVersionLabel
	acceleratorTypeLabelKey   = "neutree.ai/accelerator-type"
	sshRuntimeImageRepository = v1.NeutreeServeImageName
)

type availableClusterVersionsResponse struct {
	AvailableVersions     []string `json:"available_versions"`
	DefaultClusterVersion *string  `json:"default_cluster_version"`
}

type availableClusterVersion struct {
	name    string
	version *semver.Version
}

// getAvailableClusterVersions returns exact Profiles whose declared material
// exists in the caller-selected target registry.
func getAvailableClusterVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := strings.TrimSpace(c.Query("workspace"))
		registryName := strings.TrimSpace(c.Query("image_registry"))

		if workspace == "" || registryName == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "workspace and image_registry are required"})
			return
		}

		clusterType, err := requiredClusterType(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		if deps == nil || deps.Storage == nil || deps.ImageService == nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": "storage and image service are required"})
			return
		}

		registries, err := deps.Storage.ListImageRegistry(storage.ListOption{Filters: []storage.Filter{
			{Column: "metadata->>workspace", Operator: "eq", Value: workspace},
			{Column: "metadata->>name", Operator: "eq", Value: registryName},
		}})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list image registries: %v", err)})
			return
		}

		if len(registries) != 1 || registries[0].Spec == nil {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("image registry %s/%s not found", workspace, registryName)})
			return
		}

		imageRegistry := &registries[0]

		imagePrefix, err := util.GetImagePrefix(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve image registry: %v", err)})
			return
		}

		username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to resolve image registry auth: %v", err)})
			return
		}

		registryAuth := authn.FromConfig(authn.AuthConfig{Username: username, Password: password})
		useHTTP := util.IsHTTPRegistryURL(imageRegistry.Spec.URL)

		profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list cluster profiles: %v", err)})
			return
		}

		defaultVersion, err := parseClusterVersion(info.Spec.DefaultClusterVersion)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("invalid default cluster version: %v", err)})
			return
		}

		compatible := compatibleClusterBaselines(info.Spec.CompatibleClusterBaselines)

		var sshAcceleratorVersions map[string]struct{}
		if clusterType == v1.SSHClusterType {
			sshAcceleratorVersions, err = discoverSSHAcceleratorVersions(deps.ImageService, imagePrefix, registryAuth, useHTTP)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to check SSH accelerator images: %v", err)})
				return
			}
		}

		versions := make([]availableClusterVersion, 0, len(profiles))
		defaultAvailable := false

		for index := range profiles {
			profile := &profiles[index]
			if profile.GetName() == "" || profile.Spec == nil {
				continue
			}

			components, found := profile.Spec.ComponentsFor(clusterType)
			if !found {
				continue
			}

			version, err := parseClusterVersion(profile.GetName())
			if err != nil || version.GreaterThan(defaultVersion) {
				continue
			}

			minor, err := releaseinfo.NormalizeClusterMinor(profile.GetName())
			if err != nil || !compatible[minor] {
				continue
			}

			available, err := checkProfileImages(deps.ImageService, imagePrefix, registryAuth, useHTTP, profile.GetName(), clusterType, components, sshAcceleratorVersions)
			if err != nil {
				c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to check images for %s: %v", profile.GetName(), err)})
				return
			}

			if !available {
				continue
			}

			versions = append(versions, availableClusterVersion{name: profile.GetName(), version: version})

			if profile.GetName() == info.Spec.DefaultClusterVersion {
				defaultAvailable = true
			}
		}

		sort.Slice(versions, func(i, j int) bool {
			if versions[i].version.Equal(versions[j].version) {
				return versions[i].name < versions[j].name
			}

			return versions[i].version.LessThan(versions[j].version)
		})

		available := make([]string, 0, len(versions))
		for _, version := range versions {
			available = append(available, version.name)
		}

		var defaultResponse *string

		if defaultAvailable {
			value := info.Spec.DefaultClusterVersion
			defaultResponse = &value
		}

		c.JSON(http.StatusOK, availableClusterVersionsResponse{AvailableVersions: available, DefaultClusterVersion: defaultResponse})
	}
}

func checkProfileImages(
	service registry.ImageService,
	imagePrefix string,
	auth authn.Authenticator,
	useHTTP bool,
	clusterVersion string,
	clusterType string,
	components v1.ClusterProfileComponents,
	sshAcceleratorVersions map[string]struct{},
) (bool, error) {
	if clusterType == v1.SSHClusterType {
		if _, found := sshAcceleratorVersions[clusterVersion]; !found {
			return false, nil
		}

		return checkExactImages(service, imagePrefix, auth, useHTTP, []v1.ImageRef{components.NodeAgent, components.NodeExporter, components.VMAgent})
	}

	return checkExactImages(service, imagePrefix, auth, useHTTP, []v1.ImageRef{
		components.KubernetesRuntime,
		components.Router,
		components.NodeAgent,
		components.NodeExporter,
		components.VMAgent,
		components.KubeStateMetrics,
	})
}

func discoverSSHAcceleratorVersions(service registry.ImageService, imagePrefix string, auth authn.Authenticator, useHTTP bool) (map[string]struct{}, error) {
	serveRepo := util.RewriteImageRef(imagePrefix, sshRuntimeImageRepository)

	tags, err := service.ListImageTags(serveRepo, auth, useHTTP)
	if err != nil {
		return nil, err
	}

	versions := make(map[string]struct{})

	for _, tag := range tags {
		labels, err := service.GetImageLabels(util.RewriteImageRef(imagePrefix, sshRuntimeImageRepository+":"+tag), auth, useHTTP)
		if err != nil {
			return nil, err
		}

		version := strings.TrimSpace(labels[clusterVersionLabelKey])
		if version == "" || strings.TrimSpace(labels[acceleratorTypeLabelKey]) == "" {
			continue
		}

		versions[version] = struct{}{}
	}

	return versions, nil
}

func checkExactImages(service registry.ImageService, imagePrefix string, auth authn.Authenticator, useHTTP bool, refs []v1.ImageRef) (bool, error) {
	for _, image := range refs {
		if strings.TrimSpace(image.Image) == "" || strings.TrimSpace(image.Tag) == "" {
			return false, nil
		}

		exists, err := service.CheckImageExists(util.RewriteImageRef(imagePrefix, image.Image+":"+image.Tag), auth, useHTTP)
		if err != nil {
			return false, err
		}

		if !exists {
			return false, nil
		}
	}

	return true, nil
}

func requiredClusterType(c *gin.Context) (string, error) {
	clusterType := c.Query("cluster_type")
	if !v1.IsSupportedClusterType(clusterType) {
		return "", fmt.Errorf("cluster_type must be one of %q or %q", v1.SSHClusterType, v1.KubernetesClusterType)
	}

	return clusterType, nil
}

func currentReleaseInfo(deps *Dependencies) (*v1.ReleaseInfo, error) {
	if deps == nil || deps.ReleaseInfoProvider == nil {
		return nil, fmt.Errorf("release info provider is required")
	}

	if deps.Storage == nil {
		return nil, fmt.Errorf("storage is required")
	}

	info, err := deps.ReleaseInfoProvider.Current()
	if err != nil {
		return nil, err
	}

	if info == nil || info.Metadata == nil || info.Spec == nil {
		return nil, fmt.Errorf("release info metadata and spec are required")
	}

	return info, nil
}

func compatibleClusterBaselines(baselines []string) map[string]bool {
	compatible := make(map[string]bool, len(baselines))

	for _, baseline := range baselines {
		baseline = strings.TrimSpace(baseline)
		if baseline == "" {
			continue
		}

		compatible[baseline] = true
	}

	return compatible
}

func parseClusterVersion(version string) (*semver.Version, error) {
	if strings.TrimSpace(version) != version || !strings.HasPrefix(version, "v") {
		return nil, fmt.Errorf("cluster version %q must use v prefix", version)
	}

	return semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
}
