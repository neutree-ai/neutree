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

type availableClusterVersionsResponse struct {
	AvailableVersions     []string `json:"available_versions"`
	DefaultClusterVersion *string  `json:"default_cluster_version"`
}

type availableClusterVersionsRequest struct {
	workspace    string
	registryName string
	clusterType  string
}

type availableClusterVersionsTarget struct {
	releaseInfo *v1.ReleaseInfo
	profiles    []v1.ClusterProfile
	imagePrefix string
	auth        authn.Authenticator
	useHTTP     bool
}

type availableClusterVersion struct {
	name    string
	version *semver.Version
}

type availableClusterVersionsError struct {
	status  int
	message string
}

func (err *availableClusterVersionsError) Error() string {
	return err.message
}

// getAvailableClusterVersions returns exact Profiles whose declared material
// is available in the caller-selected target registry.
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
		clusterType:  strings.TrimSpace(c.Query("cluster_type")),
	}
	if request.workspace == "" || request.registryName == "" {
		return availableClusterVersionsRequest{}, newAvailableClusterVersionsError(
			http.StatusBadRequest,
			"workspace and image_registry are required",
		)
	}

	if !v1.IsSupportedClusterType(request.clusterType) {
		return availableClusterVersionsRequest{}, newAvailableClusterVersionsError(
			http.StatusBadRequest,
			fmt.Sprintf("cluster_type must be one of %q or %q", v1.SSHClusterType, v1.KubernetesClusterType),
		)
	}

	return request, nil
}

func resolveAvailableClusterVersions(
	deps *Dependencies,
	request availableClusterVersionsRequest,
) (availableClusterVersionsResponse, error) {
	target, err := loadAvailableClusterVersionsTarget(deps, request)
	if err != nil {
		return availableClusterVersionsResponse{}, err
	}

	sshVersionsByRepo := map[string]map[string]struct{}{}

	versions := make([]availableClusterVersion, 0, len(target.profiles))
	defaultAvailable := false
	for index := range target.profiles {
		profile := &target.profiles[index]
		if err := releaseprofile.ValidateClusterVersionCompatibility(target.releaseInfo, profile.GetName()); err != nil {
			continue
		}

		components, _ := profile.Spec.ComponentsFor(request.clusterType)
		available, err := profileImagesAvailable(
			deps.ImageService,
			target,
			request.clusterType,
			profile.GetName(),
			components,
			sshVersionsByRepo,
		)
		if err != nil {
			return availableClusterVersionsResponse{}, newAvailableClusterVersionsError(
				http.StatusInternalServerError,
				fmt.Sprintf("failed to check images for %s: %v", profile.GetName(), err),
			)
		}
		if !available {
			continue
		}

		version, err := semver.StrictNewVersion(strings.TrimPrefix(profile.GetName(), "v"))
		if err != nil {
			return availableClusterVersionsResponse{}, newAvailableClusterVersionsError(
				http.StatusInternalServerError,
				fmt.Sprintf("invalid stored cluster profile version %q: %v", profile.GetName(), err),
			)
		}

		versions = append(versions, availableClusterVersion{name: profile.GetName(), version: version})
		defaultAvailable = defaultAvailable || profile.GetName() == target.releaseInfo.Spec.DefaultClusterVersion
	}

	return availableClusterVersionsResponse{
		AvailableVersions:     sortedAvailableClusterVersions(versions),
		DefaultClusterVersion: availableDefaultClusterVersion(target.releaseInfo, defaultAvailable),
	}, nil
}

func loadAvailableClusterVersionsTarget(
	deps *Dependencies,
	request availableClusterVersionsRequest,
) (*availableClusterVersionsTarget, error) {
	if deps == nil || deps.Storage == nil || deps.ImageService == nil || deps.ReleaseInfoProvider == nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			"storage, image service, and release info provider are required",
		)
	}

	info, err := deps.ReleaseInfoProvider.Current()
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to get release info: %v", err),
		)
	}

	registries, err := deps.Storage.ListImageRegistry(storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: request.workspace},
		{Column: "metadata->>name", Operator: "eq", Value: request.registryName},
	}})
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to list image registries: %v", err),
		)
	}
	if len(registries) == 0 {
		return nil, newAvailableClusterVersionsError(
			http.StatusNotFound,
			fmt.Sprintf("image registry %s/%s not found", request.workspace, request.registryName),
		)
	}
	if len(registries) != 1 || registries[0].Spec == nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			"expected one valid image registry",
		)
	}

	imageRegistry := &registries[0]
	imagePrefix, err := util.GetImagePrefix(imageRegistry)
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to resolve image registry: %v", err),
		)
	}

	username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to resolve image registry auth: %v", err),
		)
	}

	auth := authn.Anonymous
	if username != "" || password != "" {
		auth = authn.FromConfig(authn.AuthConfig{Username: username, Password: password})
	}

	profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to list cluster profiles: %v", err),
		)
	}

	return &availableClusterVersionsTarget{
		releaseInfo: info,
		profiles:    profiles,
		imagePrefix: imagePrefix,
		auth:        auth,
		useHTTP:     util.IsHTTPRegistryURL(imageRegistry.Spec.URL),
	}, nil
}

func availableSSHAcceleratorVersions(
	target *availableClusterVersionsTarget,
	rayRuntimeImage string,
	service registry.ImageService,
) (map[string]struct{}, error) {
	// RayRuntime is the SSH accelerator-semantic component. Its labels map an
	// accelerator-specific artifact back to the Profile's exact version.
	runtimeRepository := util.RewriteImageRef(target.imagePrefix, strings.TrimSpace(rayRuntimeImage))
	tags, err := service.ListImageTags(runtimeRepository, target.auth, target.useHTTP)
	if err != nil {
		return nil, newAvailableClusterVersionsError(
			http.StatusInternalServerError,
			fmt.Sprintf("failed to check SSH accelerator images: %v", err),
		)
	}

	versions := make(map[string]struct{})
	for _, tag := range tags {
		labels, err := service.GetImageLabels(runtimeRepository+":"+tag, target.auth, target.useHTTP)
		if err != nil {
			return nil, newAvailableClusterVersionsError(
				http.StatusInternalServerError,
				fmt.Sprintf("failed to read SSH accelerator image labels: %v", err),
			)
		}

		version := strings.TrimSpace(labels[v1.ImageLabelVersion])
		acceleratorType := strings.TrimSpace(labels[v1.ImageLabelAcceleratorType])
		if version == "" || acceleratorType == "" {
			continue
		}

		versions[version] = struct{}{}
	}

	return versions, nil
}

func profileImagesAvailable(
	service registry.ImageService,
	target *availableClusterVersionsTarget,
	clusterType string,
	clusterVersion string,
	components v1.ClusterProfileComponents,
	sshVersionsByRepo map[string]map[string]struct{},
) (bool, error) {
	switch clusterType {
	case v1.SSHClusterType:
		runtimeRepository := util.RewriteImageRef(target.imagePrefix, strings.TrimSpace(components.RayRuntime.Image))
		sshVersions, found := sshVersionsByRepo[runtimeRepository]
		if !found {
			var err error
			sshVersions, err = availableSSHAcceleratorVersions(target, components.RayRuntime.Image, service)
			if err != nil {
				return false, err
			}
			sshVersionsByRepo[runtimeRepository] = sshVersions
		}

		if _, found := sshVersions[clusterVersion]; !found {
			return false, nil
		}

		return exactImagesAvailable(service, target, []v1.ImageRef{
			components.NodeAgent,
			components.NodeExporter,
			components.VMAgent,
		})
	case v1.KubernetesClusterType:
		return exactImagesAvailable(service, target, []v1.ImageRef{
			components.KubernetesRuntime,
			components.Router,
			components.NodeAgent,
			components.NodeExporter,
			components.VMAgent,
			components.KubeStateMetrics,
		})
	default:
		return false, fmt.Errorf("unsupported cluster type %q", clusterType)
	}
}

func exactImagesAvailable(
	service registry.ImageService,
	target *availableClusterVersionsTarget,
	refs []v1.ImageRef,
) (bool, error) {
	for _, ref := range refs {
		image := strings.TrimSpace(ref.Image)
		tag := strings.TrimSpace(ref.Tag)
		if image == "" || tag == "" {
			return false, fmt.Errorf("cluster profile image and tag are required")
		}

		exists, err := service.CheckImageExists(
			util.RewriteImageRef(target.imagePrefix, image+":"+tag),
			target.auth,
			target.useHTTP,
		)
		if err != nil {
			return false, err
		}
		if !exists {
			return false, nil
		}
	}

	return true, nil
}

func sortedAvailableClusterVersions(versions []availableClusterVersion) []string {
	sort.Slice(versions, func(left, right int) bool {
		if versions[left].version.Equal(versions[right].version) {
			return versions[left].name < versions[right].name
		}

		return versions[left].version.LessThan(versions[right].version)
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
	response := &availableClusterVersionsError{
		status:  http.StatusInternalServerError,
		message: "internal server error",
	}
	if typed, ok := err.(*availableClusterVersionsError); ok {
		response = typed
	}
	if response.status >= http.StatusInternalServerError {
		response.message = "internal server error"
	}

	c.JSON(response.status, gin.H{"error": response.message})
}
