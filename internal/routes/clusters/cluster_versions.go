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
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
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

type availableClusterVersionsRequest struct {
	workspace    string
	registryName string
	clusterType  string
}

type availableClusterVersionsTarget struct {
	info         *v1.ReleaseInfo
	profiles     []v1.ClusterProfile
	imagePrefix  string
	registryAuth authn.Authenticator
	useHTTP      bool
}

type availableClusterVersionsError struct {
	status  int
	message string
}

func (err *availableClusterVersionsError) Error() string {
	return err.message
}

// getAvailableClusterVersions returns exact Profiles whose declared material
// exists in the caller-selected target registry.
func getAvailableClusterVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		request, err := parseAvailableClusterVersionsRequest(c)
		if err != nil {
			writeAvailableClusterVersionsError(c, err)

			return
		}

		response, err := resolveAvailableClusterVersions(deps, request)
		if err != nil {
			writeAvailableClusterVersionsError(c, err)

			return
		}

		c.JSON(http.StatusOK, response)
	}
}

func parseAvailableClusterVersionsRequest(c *gin.Context) (availableClusterVersionsRequest, error) {
	request := availableClusterVersionsRequest{
		workspace:    strings.TrimSpace(c.Query("workspace")),
		registryName: strings.TrimSpace(c.Query("image_registry")),
	}

	if request.workspace == "" || request.registryName == "" {
		return availableClusterVersionsRequest{}, newAvailableClusterVersionsError(http.StatusBadRequest, "workspace and image_registry are required")
	}

	clusterType, err := requiredClusterType(c)
	if err != nil {
		return availableClusterVersionsRequest{}, newAvailableClusterVersionsError(http.StatusBadRequest, err.Error())
	}

	request.clusterType = clusterType

	return request, nil
}

func resolveAvailableClusterVersions(deps *Dependencies, request availableClusterVersionsRequest) (availableClusterVersionsResponse, error) {
	target, err := loadAvailableClusterVersionsTarget(deps, request)
	if err != nil {
		return availableClusterVersionsResponse{}, err
	}

	defaultVersion, err := parseClusterVersion(target.info.Spec.DefaultClusterVersion)
	if err != nil {
		return availableClusterVersionsResponse{}, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("invalid default cluster version: %v", err))
	}

	compatible := compatibleClusterBaselines(target.info.Spec.CompatibleClusterBaselines)

	sshAcceleratorVersions, err := availableSSHAcceleratorVersions(deps.ImageService, target, request.clusterType)
	if err != nil {
		return availableClusterVersionsResponse{}, err
	}

	versions, defaultAvailable, err := selectAvailableClusterVersions(deps.ImageService, target, request.clusterType, defaultVersion, compatible, sshAcceleratorVersions)
	if err != nil {
		return availableClusterVersionsResponse{}, err
	}

	return availableClusterVersionsResponse{
		AvailableVersions:     sortedAvailableClusterVersionNames(versions),
		DefaultClusterVersion: availableDefaultClusterVersion(target.info, defaultAvailable),
	}, nil
}

func loadAvailableClusterVersionsTarget(deps *Dependencies, request availableClusterVersionsRequest) (*availableClusterVersionsTarget, error) {
	info, err := currentReleaseInfo(deps)
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to get release info: %v", err))
	}

	if deps == nil || deps.Storage == nil || deps.ImageService == nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, "storage and image service are required")
	}

	registries, err := deps.Storage.ListImageRegistry(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: request.workspace},
		{Column: "metadata->>name", Operator: "eq", Value: request.registryName},
	}})
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to list image registries: %v", err))
	}

	if len(registries) != 1 || registries[0].Spec == nil {
		return nil, newAvailableClusterVersionsError(http.StatusNotFound, fmt.Sprintf("image registry %s/%s not found", request.workspace, request.registryName))
	}

	imageRegistry := &registries[0]

	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to resolve image registry: %v", err))
	}

	username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to resolve image registry auth: %v", err))
	}

	profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to list cluster profiles: %v", err))
	}

	return &availableClusterVersionsTarget{
		info:         info,
		profiles:     profiles,
		imagePrefix:  imagePrefix,
		registryAuth: authn.FromConfig(authn.AuthConfig{Username: username, Password: password}),
		useHTTP:      util.IsHTTPRegistryURL(imageRegistry.Spec.URL),
	}, nil
}

func availableSSHAcceleratorVersions(service registry.ImageService, target *availableClusterVersionsTarget, clusterType string) (map[string]struct{}, error) {
	if clusterType != v1.SSHClusterType {
		return nil, nil
	}

	versions, err := discoverSSHAcceleratorVersions(service, target.imagePrefix, target.registryAuth, target.useHTTP)
	if err != nil {
		return nil, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to check SSH accelerator images: %v", err))
	}

	return versions, nil
}

func selectAvailableClusterVersions(
	service registry.ImageService,
	target *availableClusterVersionsTarget,
	clusterType string,
	defaultVersion *semver.Version,
	compatible map[string]bool,
	sshAcceleratorVersions map[string]struct{},
) ([]availableClusterVersion, bool, error) {
	versions := make([]availableClusterVersion, 0, len(target.profiles))
	defaultAvailable := false

	for index := range target.profiles {
		profile := &target.profiles[index]
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

		minor, err := releaseprofile.NormalizeClusterMinor(profile.GetName())
		if err != nil || !compatible[minor] {
			continue
		}

		available, err := checkProfileImages(
			service,
			target.imagePrefix,
			target.registryAuth,
			target.useHTTP,
			profile.GetName(),
			clusterType,
			components,
			sshAcceleratorVersions,
		)
		if err != nil {
			return nil, false, newAvailableClusterVersionsError(http.StatusInternalServerError, fmt.Sprintf("failed to check images for %s: %v", profile.GetName(), err))
		}

		if !available {
			continue
		}

		versions = append(versions, availableClusterVersion{name: profile.GetName(), version: version})
		defaultAvailable = defaultAvailable || profile.GetName() == target.info.Spec.DefaultClusterVersion
	}

	return versions, defaultAvailable, nil
}

func sortedAvailableClusterVersionNames(versions []availableClusterVersion) []string {
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

	return available
}

func availableDefaultClusterVersion(info *v1.ReleaseInfo, defaultAvailable bool) *string {
	if !defaultAvailable {
		return nil
	}

	value := info.Spec.DefaultClusterVersion

	return &value
}

func newAvailableClusterVersionsError(status int, message string) *availableClusterVersionsError {
	return &availableClusterVersionsError{status: status, message: message}
}

func writeAvailableClusterVersionsError(c *gin.Context, err error) {
	response := &availableClusterVersionsError{status: http.StatusInternalServerError, message: err.Error()}
	if typed, ok := err.(*availableClusterVersionsError); ok {
		response = typed
	}

	c.JSON(response.status, gin.H{"error": response.message})
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
