package releaseinfo

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// CurrentBaselineStore persists the ReleaseInfo and ClusterProfile pair for
// the current control-plane baseline. It is independent from the legacy seed
// Store so the new semantic release path can be introduced incrementally.
type CurrentBaselineStore interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
	CreateReleaseInfo(*v1.ReleaseInfo) error
	UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error
	ListClusterProfile() ([]v1.ClusterProfile, error)
	CreateClusterProfile(*v1.ClusterProfile) error
	UpdateClusterProfile(id string, profile *v1.ClusterProfile) error
}

// SynchronizeCurrentBaseline creates or overwrites the current semantic
// ReleaseInfo and full-version ClusterProfile. Historical profile seeds are
// created only when first introducing the v1.2.0 baseline.
func SynchronizeCurrentBaseline(
	store CurrentBaselineStore,
	baseline string,
	releaseInfoBuilder ReleaseInfoBuilder,
	clusterProfileBuilder CurrentClusterProfileBuilder,
) error {
	if store == nil {
		return fmt.Errorf("current baseline store is required")
	}
	if releaseInfoBuilder == nil {
		return fmt.Errorf("release info builder is required")
	}
	if clusterProfileBuilder == nil {
		return fmt.Errorf("cluster profile builder is required")
	}

	info, err := releaseInfoBuilder.BuildReleaseInfo(baseline)
	if err != nil {
		return fmt.Errorf("build release info: %w", err)
	}
	if err := validateCurrentReleaseInfoBuilderOutput(baseline, info); err != nil {
		return err
	}

	profile, err := clusterProfileBuilder.BuildClusterProfile(baseline)
	if err != nil {
		return fmt.Errorf("build cluster profile: %w", err)
	}
	if err := validateCurrentClusterProfileBuilderOutput(baseline, profile); err != nil {
		return err
	}

	infos, err := store.ListReleaseInfo()
	if err != nil {
		return fmt.Errorf("list release infos: %w", err)
	}
	profiles, err := store.ListClusterProfile()
	if err != nil {
		return fmt.Errorf("list cluster profiles: %w", err)
	}

	if existing := releaseInfoByName(infos, baseline); existing == nil {
		if err := store.CreateReleaseInfo(deepCopyReleaseInfo(info)); err != nil {
			return fmt.Errorf("create release info: %w", err)
		}
	} else if err := store.UpdateReleaseInfo(existing.GetID(), deepCopyReleaseInfo(info)); err != nil {
		return fmt.Errorf("update release info: %w", err)
	}

	if existing := clusterProfileByName(profiles, baseline); existing == nil {
		if err := store.CreateClusterProfile(deepCopyClusterProfile(profile)); err != nil {
			return fmt.Errorf("create cluster profile: %w", err)
		}
	} else if err := store.UpdateClusterProfile(existing.GetID(), deepCopyClusterProfile(profile)); err != nil {
		return fmt.Errorf("update cluster profile: %w", err)
	}

	if baseline != "v1.2.0" {
		return nil
	}

	for _, historicalVersion := range []string{"v1.1.0", "v1.1.1"} {
		if clusterProfileByName(profiles, historicalVersion) != nil {
			continue
		}

		historical := communityHistoricalClusterProfile(historicalVersion)
		if err := store.CreateClusterProfile(deepCopyClusterProfile(historical)); err != nil {
			return fmt.Errorf("create historical cluster profile %s: %w", historicalVersion, err)
		}
	}

	return nil
}

func validateCurrentReleaseInfoBuilderOutput(baseline string, info *v1.ReleaseInfo) error {
	if info == nil || info.Metadata == nil || info.Spec == nil {
		return fmt.Errorf("release info builder output requires release info, metadata, and spec")
	}
	if info.Metadata.Name != baseline {
		return fmt.Errorf("release info builder output name %q must match requested baseline %q", info.Metadata.Name, baseline)
	}
	if info.Spec.Channel != "" || info.Spec.BuildIdentity != "" || info.Spec.ClusterVersions != nil || info.Status != nil {
		return fmt.Errorf("release info builder output must not set legacy fields")
	}

	return nil
}

func validateCurrentClusterProfileBuilderOutput(baseline string, profile *v1.ClusterProfile) error {
	if profile == nil || profile.Metadata == nil || profile.Spec == nil {
		return fmt.Errorf("cluster profile builder output requires cluster profile, metadata, and spec")
	}
	if profile.Metadata.Name != baseline {
		return fmt.Errorf("cluster profile builder output name %q must match requested baseline %q", profile.Metadata.Name, baseline)
	}

	return nil
}

func releaseInfoByName(infos []v1.ReleaseInfo, name string) *v1.ReleaseInfo {
	for index := range infos {
		if infos[index].GetName() == name {
			return &infos[index]
		}
	}

	return nil
}

func clusterProfileByName(profiles []v1.ClusterProfile, name string) *v1.ClusterProfile {
	for index := range profiles {
		if profiles[index].GetName() == name {
			return &profiles[index]
		}
	}

	return nil
}

func deepCopyReleaseInfo(info *v1.ReleaseInfo) *v1.ReleaseInfo {
	copy := *info
	copy.Metadata = deepCopyMetadata(info.Metadata)
	copy.Spec = deepCopyReleaseInfoSpec(info.Spec)
	if info.Status != nil {
		status := *info.Status
		copy.Status = &status
	}

	return &copy
}

func deepCopyReleaseInfoSpec(spec *v1.ReleaseInfoSpec) *v1.ReleaseInfoSpec {
	if spec == nil {
		return nil
	}

	copy := *spec
	copy.CompatibleClusterBaselines = append([]string(nil), spec.CompatibleClusterBaselines...)
	if spec.ClusterVersions != nil {
		copy.ClusterVersions = make([]v1.ReleaseInfoClusterVersion, len(spec.ClusterVersions))
		for index := range spec.ClusterVersions {
			copy.ClusterVersions[index] = deepCopyReleaseInfoClusterVersion(spec.ClusterVersions[index])
		}
	}

	return &copy
}

func deepCopyReleaseInfoClusterVersion(version v1.ReleaseInfoClusterVersion) v1.ReleaseInfoClusterVersion {
	copy := version
	copy.UpgradeTo = append([]string(nil), version.UpgradeTo...)
	copy.Components = copyStringMap(version.Components)
	if version.AcceleratorComponents != nil {
		copy.AcceleratorComponents = make(map[string]map[string]string, len(version.AcceleratorComponents))
		for accelerator, components := range version.AcceleratorComponents {
			copy.AcceleratorComponents[accelerator] = copyStringMap(components)
		}
	}

	return copy
}

func deepCopyClusterProfile(profile *v1.ClusterProfile) *v1.ClusterProfile {
	copy := *profile
	copy.Metadata = deepCopyMetadata(profile.Metadata)
	if profile.Spec != nil {
		spec := *profile.Spec
		copy.Spec = &spec
	}

	return &copy
}

func deepCopyMetadata(metadata *v1.Metadata) *v1.Metadata {
	if metadata == nil {
		return nil
	}

	copy := *metadata
	copy.Labels = copyStringMap(metadata.Labels)
	copy.Annotations = copyStringMap(metadata.Annotations)
	return &copy
}

func copyStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}

	return copy
}
