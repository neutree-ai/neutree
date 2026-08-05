package clusters

import (
	"fmt"
	"net/http"
	"sort"
	"strings"

	"github.com/Masterminds/semver/v3"
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type availableClusterVersionsResponse struct {
	AvailableVersions []string `json:"available_versions"`
}

type availableClusterVersion struct {
	name    string
	version *semver.Version
}

// getAvailableClusterVersions returns imported ClusterProfiles that are
// compatible with the running control-plane ReleaseInfo.
func getAvailableClusterVersions(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		info, err := currentReleaseInfo(deps)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		currentBaseline, err := currentControlPlaneMinor(info)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get release info: %v", err)})
			return
		}

		profiles, err := deps.Storage.ListClusterProfile(storage.ListOption{})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to list cluster profiles: %v", err)})
			return
		}

		compatible := compatibleClusterBaselines(info.Spec.CompatibleClusterBaselines)
		versions := make([]availableClusterVersion, 0, len(profiles))

		for _, profile := range profiles {
			profileName := profile.GetName()

			minor, err := releaseinfo.NormalizeClusterMinor(profileName)
			if err != nil || !compatible[minor] || !clusterMinorAtLeast(minor, currentBaseline) {
				continue
			}

			version, err := parseClusterVersion(profileName)
			if err != nil {
				continue
			}

			versions = append(versions, availableClusterVersion{name: profileName, version: version})
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

		c.JSON(http.StatusOK, availableClusterVersionsResponse{AvailableVersions: available})
	}
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

func currentControlPlaneMinor(info *v1.ReleaseInfo) (string, error) {
	return releaseinfo.NormalizeClusterMinor(info.Metadata.Name)
}

func compatibleClusterBaselines(baselines []string) map[string]bool {
	compatible := make(map[string]bool, len(baselines))
	for _, baseline := range baselines {
		compatible[baseline] = true
	}

	return compatible
}

func clusterMinorAtLeast(candidate, current string) bool {
	candidateVersion, err := semver.StrictNewVersion(strings.TrimPrefix(candidate, "v") + ".0")
	if err != nil {
		return false
	}

	currentVersion, err := semver.StrictNewVersion(strings.TrimPrefix(current, "v") + ".0")
	if err != nil {
		return false
	}

	return !candidateVersion.LessThan(currentVersion)
}

func parseClusterVersion(version string) (*semver.Version, error) {
	if !strings.HasPrefix(version, "v") {
		return nil, fmt.Errorf("cluster version %q must use v prefix", version)
	}

	return semver.StrictNewVersion(strings.TrimPrefix(version, "v"))
}
