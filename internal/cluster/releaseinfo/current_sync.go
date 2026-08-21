package releaseinfo

import (
	"fmt"
	"regexp"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

var compatibleClusterBaselinePattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

// CurrentBaselineStore persists the ReleaseInfo and ClusterProfile pair for
// the current control-plane baseline.
type CurrentBaselineStore interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
	CreateReleaseInfo(*v1.ReleaseInfo) error
	UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error
	ListClusterProfile() ([]v1.ClusterProfile, error)
	CreateClusterProfile(*v1.ClusterProfile) error
	UpdateClusterProfile(id string, profile *v1.ClusterProfile) error
}

// SynchronizeCurrentBaseline creates or overwrites the current semantic
// ReleaseInfo and one full-version ClusterProfile for each supported cluster
// family. Historical profile seeds are created only when first introducing the
// v1.2.0 baseline.
func SynchronizeCurrentBaseline(
	store CurrentBaselineStore,
	baseline string,
	releaseInfoBuilder releaseprofile.ReleaseInfoBuilder,
	clusterProfileBuilder releaseprofile.CurrentClusterProfileBuilder,
) error {
	if _, err := parseStableReleaseInfoBaseline(baseline); err != nil {
		return fmt.Errorf("invalid stable release info baseline %q: %w", baseline, err)
	}

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

	currentProfiles := make([]*v1.ClusterProfile, 0, 2)

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		profile, err := clusterProfileBuilder.BuildClusterProfile(baseline, clusterType)
		if err != nil {
			return fmt.Errorf("build %s cluster profile: %w", clusterType, err)
		}

		if err := validateCurrentClusterProfileBuilderOutput(baseline, clusterType, profile); err != nil {
			return err
		}

		currentProfiles = append(currentProfiles, profile)
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

	for _, profile := range currentProfiles {
		if existing := clusterProfileByIdentity(profiles, baseline, profile.GetClusterType()); existing == nil {
			if err := store.CreateClusterProfile(deepCopyClusterProfile(profile)); err != nil {
				return fmt.Errorf("create %s cluster profile: %w", profile.GetClusterType(), err)
			}
		} else if err := store.UpdateClusterProfile(existing.GetID(), deepCopyClusterProfile(profile)); err != nil {
			return fmt.Errorf("update %s cluster profile: %w", profile.GetClusterType(), err)
		}
	}

	if baseline != "v1.2.0" {
		return nil
	}

	for _, historicalVersion := range []string{"v1.1.0", "v1.1.1"} {
		for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
			if clusterProfileByIdentity(profiles, historicalVersion, clusterType) != nil {
				continue
			}

			historical, err := releaseprofile.CommunityHistoricalClusterProfile(historicalVersion, clusterType)
			if err != nil {
				return fmt.Errorf("build historical %s cluster profile %s: %w", clusterType, historicalVersion, err)
			}

			if err := store.CreateClusterProfile(deepCopyClusterProfile(historical)); err != nil {
				return fmt.Errorf("create historical %s cluster profile %s: %w", clusterType, historicalVersion, err)
			}
		}
	}

	return nil
}

func validateCurrentReleaseInfoBuilderOutput(baseline string, info *v1.ReleaseInfo) error {
	if info == nil || info.Metadata == nil || info.Spec == nil {
		return fmt.Errorf("release info builder output requires release info, metadata, and spec")
	}

	if info.APIVersion != "v1" {
		return fmt.Errorf("release info builder output api version must be v1")
	}

	if info.Kind != v1.ReleaseInfoKind {
		return fmt.Errorf("release info builder output kind must be %s", v1.ReleaseInfoKind)
	}

	if info.Metadata.Name != baseline {
		return fmt.Errorf("release info builder output name %q must match requested baseline %q", info.Metadata.Name, baseline)
	}

	if len(info.Spec.CompatibleClusterBaselines) == 0 {
		return fmt.Errorf("release info builder output compatible cluster baselines are required")
	}

	seenBaselines := make(map[string]struct{}, len(info.Spec.CompatibleClusterBaselines))

	for _, compatibleBaseline := range info.Spec.CompatibleClusterBaselines {
		if !compatibleClusterBaselinePattern.MatchString(compatibleBaseline) {
			return fmt.Errorf("invalid compatible cluster baseline %q", compatibleBaseline)
		}

		if _, found := seenBaselines[compatibleBaseline]; found {
			return fmt.Errorf("duplicate compatible cluster baseline %q", compatibleBaseline)
		}

		seenBaselines[compatibleBaseline] = struct{}{}
	}

	return nil
}

func validateCurrentClusterProfileBuilderOutput(baseline, clusterType string, profile *v1.ClusterProfile) error {
	if profile == nil || profile.Metadata == nil || profile.Spec == nil {
		return fmt.Errorf("cluster profile builder output requires cluster profile, metadata, and spec")
	}

	if profile.APIVersion != "v1" {
		return fmt.Errorf("cluster profile builder output api version must be v1")
	}

	if profile.Kind != v1.ClusterProfileKind {
		return fmt.Errorf("cluster profile builder output kind must be %s", v1.ClusterProfileKind)
	}

	if profile.Metadata.Name != baseline {
		return fmt.Errorf("cluster profile builder output name %q must match requested baseline %q", profile.Metadata.Name, baseline)
	}

	if profile.Spec.ClusterType != clusterType {
		return fmt.Errorf("cluster profile builder output type %q must match requested type %q", profile.Spec.ClusterType, clusterType)
	}

	for _, component := range requiredClusterProfileComponents(profile.Spec.ClusterType, profile.Spec.Components) {
		if strings.TrimSpace(component.ref.Image) == "" {
			return fmt.Errorf("cluster profile builder output %s image is required", component.name)
		}

		if strings.TrimSpace(component.ref.Tag) == "" {
			return fmt.Errorf("cluster profile builder output %s tag is required", component.name)
		}
	}

	return nil
}

func requiredClusterProfileComponents(clusterType string, components v1.ClusterProfileComponents) []struct {
	name string
	ref  v1.ImageRef
} {
	switch clusterType {
	case v1.SSHClusterType:
		return []struct {
			name string
			ref  v1.ImageRef
		}{
			{name: "ray runtime", ref: components.RayRuntime},
			{name: "node agent", ref: components.NodeAgent},
			{name: "node exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
		}
	case v1.KubernetesClusterType:
		return []struct {
			name string
			ref  v1.ImageRef
		}{
			{name: "kubernetes runtime", ref: components.KubernetesRuntime},
			{name: "router", ref: components.Router},
			{name: "node agent", ref: components.NodeAgent},
			{name: "node exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
			{name: "kube state metrics", ref: components.KubeStateMetrics},
		}
	default:
		return nil
	}
}

func releaseInfoByName(infos []v1.ReleaseInfo, name string) *v1.ReleaseInfo {
	for index := range infos {
		if infos[index].GetName() == name {
			return &infos[index]
		}
	}

	return nil
}

func clusterProfileByIdentity(profiles []v1.ClusterProfile, name, clusterType string) *v1.ClusterProfile {
	for index := range profiles {
		if profiles[index].GetName() == name && profiles[index].GetClusterType() == clusterType {
			return &profiles[index]
		}
	}

	return nil
}

func deepCopyReleaseInfo(info *v1.ReleaseInfo) *v1.ReleaseInfo {
	copy := *info
	copy.Metadata = deepCopyMetadata(info.Metadata)
	copy.Spec = deepCopyReleaseInfoSpec(info.Spec)

	return &copy
}

func deepCopyReleaseInfoSpec(spec *v1.ReleaseInfoSpec) *v1.ReleaseInfoSpec {
	if spec == nil {
		return nil
	}

	copy := *spec
	copy.CompatibleClusterBaselines = append([]string(nil), spec.CompatibleClusterBaselines...)

	return &copy
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
