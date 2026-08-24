package releaseprofile

import (
	"fmt"
	"maps"
	"reflect"
	"regexp"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

var compatibleClusterBaselinePattern = regexp.MustCompile(`^v(?:0|[1-9][0-9]*)\.(?:0|[1-9][0-9]*)$`)

// CurrentBaselineStore persists the ReleaseInfo policy and ClusterProfile
// catalog for the current control-plane release.
type CurrentBaselineStore interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
	CreateReleaseInfo(*v1.ReleaseInfo) error
	UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error
	ListClusterProfile() ([]v1.ClusterProfile, error)
	CreateClusterProfile(*v1.ClusterProfile) error
}

// SynchronizeCurrentBaseline updates the current ReleaseInfo policy and
// ensures every catalog Profile exists. Existing Profiles are immutable: an
// identical replay is a no-op and any content drift fails initialization before
// changing either resource.
func SynchronizeCurrentBaseline(store CurrentBaselineStore, baseline string, builder Builder) error {
	if _, err := parseExactReleaseInfoBaseline(baseline); err != nil {
		return fmt.Errorf("invalid release info baseline %q: %w", baseline, err)
	}

	if store == nil {
		return fmt.Errorf("current baseline store is required")
	}

	if builder == nil {
		return fmt.Errorf("release profile builder is required")
	}

	info, err := builder.BuildReleaseInfo(baseline)
	if err != nil {
		return fmt.Errorf("build release info: %w", err)
	}

	if err := validateCurrentReleaseInfoBuilderOutput(baseline, info); err != nil {
		return err
	}

	profiles, err := builder.BuildClusterProfiles(baseline)
	if err != nil {
		return fmt.Errorf("build cluster profile catalog: %w", err)
	}

	if err := validateCurrentClusterProfileCatalog(info, profiles); err != nil {
		return err
	}

	infos, err := store.ListReleaseInfo()
	if err != nil {
		return fmt.Errorf("list release infos: %w", err)
	}

	persistedProfiles, err := store.ListClusterProfile()
	if err != nil {
		return fmt.Errorf("list cluster profiles: %w", err)
	}

	existingProfiles, err := clusterProfileIndexByName(persistedProfiles)
	if err != nil {
		return err
	}

	for _, profile := range profiles {
		existing := existingProfiles[profile.GetName()]
		if existing == nil {
			continue
		}

		if !ClusterProfilesSemanticallyEqual(existing, profile) {
			return fmt.Errorf("cluster profile %s content drift", profile.GetName())
		}
	}

	if existing := releaseInfoByName(infos, baseline); existing == nil {
		if err := store.CreateReleaseInfo(cloneReleaseInfo(info)); err != nil {
			return fmt.Errorf("create release info: %w", err)
		}
	} else if err := store.UpdateReleaseInfo(existing.GetID(), cloneReleaseInfo(info)); err != nil {
		return fmt.Errorf("update release info: %w", err)
	}

	for _, profile := range profiles {
		if existingProfiles[profile.GetName()] != nil {
			continue
		}

		if err := store.CreateClusterProfile(cloneClusterProfile(profile)); err != nil {
			return fmt.Errorf("create cluster profile %s: %w", profile.GetName(), err)
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

	if info.Metadata.Workspace != "" {
		return fmt.Errorf("release info builder output metadata.workspace must be empty")
	}

	if _, err := parseExactReleaseInfoBaseline(info.Metadata.Name); err != nil {
		return fmt.Errorf("invalid release info builder output name %q: %w", info.Metadata.Name, err)
	}

	if strings.TrimSpace(info.Spec.DefaultClusterVersion) == "" {
		return fmt.Errorf("release info builder output default cluster version is required")
	}

	if _, err := parseExactVPrefixedSemVer(info.Spec.DefaultClusterVersion); err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
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

	defaultMinor, err := NormalizeClusterMinor(info.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	if _, found := seenBaselines[defaultMinor]; !found {
		return fmt.Errorf("default cluster version %q has incompatible baseline %q", info.Spec.DefaultClusterVersion, defaultMinor)
	}

	return nil
}

func validateCurrentClusterProfileCatalog(info *v1.ReleaseInfo, profiles []*v1.ClusterProfile) error {
	if len(profiles) == 0 {
		return fmt.Errorf("cluster profile catalog is empty")
	}

	if info == nil || info.Spec == nil {
		return fmt.Errorf("release info metadata and spec are required")
	}

	defaultVersion, err := parseExactVPrefixedSemVer(info.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	compatibleBaselines := make(map[string]struct{}, len(info.Spec.CompatibleClusterBaselines))

	for _, baseline := range info.Spec.CompatibleClusterBaselines {
		compatibleBaselines[baseline] = struct{}{}
	}

	seenProfiles := make(map[string]struct{}, len(profiles))

	for _, profile := range profiles {
		if err := validateCurrentClusterProfileBuilderOutput(profile); err != nil {
			return err
		}

		name := profile.GetName()
		if _, found := seenProfiles[name]; found {
			return fmt.Errorf("duplicate cluster profile builder output %q", name)
		}

		seenProfiles[name] = struct{}{}

		version, err := parseExactVPrefixedSemVer(name)
		if err != nil {
			return fmt.Errorf("invalid cluster profile version %q: %w", name, err)
		}

		if version.GreaterThan(defaultVersion) {
			return fmt.Errorf("cluster profile %q exceeds default cluster version %q", name, info.Spec.DefaultClusterVersion)
		}

		minor, err := NormalizeClusterMinor(name)
		if err != nil {
			return err
		}

		if _, found := compatibleBaselines[minor]; !found {
			return fmt.Errorf("cluster profile %q has incompatible baseline %q", name, minor)
		}
	}

	if _, found := seenProfiles[info.Spec.DefaultClusterVersion]; !found {
		return fmt.Errorf("cluster profile catalog is missing default cluster version %q", info.Spec.DefaultClusterVersion)
	}

	return nil
}

func validateCurrentClusterProfileBuilderOutput(profile *v1.ClusterProfile) error {
	if profile == nil || profile.Metadata == nil || profile.Spec == nil {
		return fmt.Errorf("cluster profile builder output requires cluster profile, metadata, and spec")
	}

	if profile.APIVersion != "v1" {
		return fmt.Errorf("cluster profile builder output api version must be v1")
	}

	if profile.Kind != v1.ClusterProfileKind {
		return fmt.Errorf("cluster profile builder output kind must be %s", v1.ClusterProfileKind)
	}

	if profile.Metadata.Workspace != "" {
		return fmt.Errorf("cluster profile builder output metadata.workspace must be empty")
	}

	if _, err := parseExactVPrefixedSemVer(profile.Metadata.Name); err != nil {
		return fmt.Errorf("invalid cluster profile version %q: %w", profile.Metadata.Name, err)
	}

	for clusterType := range profile.Spec.Components {
		if !v1.IsSupportedClusterType(clusterType) {
			return fmt.Errorf("unsupported component matrix type %q", clusterType)
		}
	}

	for _, clusterType := range []string{v1.SSHClusterType, v1.KubernetesClusterType} {
		components, found := profile.Spec.ComponentsFor(clusterType)
		if !found {
			return fmt.Errorf("%s component matrix is required", clusterType)
		}

		for _, component := range requiredClusterProfileComponents(clusterType, components) {
			if strings.TrimSpace(component.ref.Image) == "" {
				return fmt.Errorf("cluster profile builder output %s image is required", component.name)
			}

			if strings.TrimSpace(component.ref.Tag) == "" {
				return fmt.Errorf("cluster profile builder output %s tag is required", component.name)
			}
		}
	}

	return nil
}

type requiredProfileComponent struct {
	name string
	ref  v1.ImageRef
}

func requiredClusterProfileComponents(clusterType string, components v1.ClusterProfileComponents) []requiredProfileComponent {
	switch clusterType {
	case v1.SSHClusterType:
		return []requiredProfileComponent{
			{name: "ray runtime", ref: components.RayRuntime},
			{name: "node agent", ref: components.NodeAgent},
			{name: "node exporter", ref: components.NodeExporter},
			{name: "vmagent", ref: components.VMAgent},
		}
	case v1.KubernetesClusterType:
		return []requiredProfileComponent{
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

func clusterProfileIndexByName(profiles []v1.ClusterProfile) (map[string]*v1.ClusterProfile, error) {
	indexed := make(map[string]*v1.ClusterProfile, len(profiles))

	for index := range profiles {
		name := profiles[index].GetName()
		if _, found := indexed[name]; found {
			return nil, fmt.Errorf("duplicate persisted cluster profile %q", name)
		}

		indexed[name] = &profiles[index]
	}

	return indexed, nil
}

// ClusterProfilesSemanticallyEqual compares only mutable protocol content and
// deliberately ignores database identifiers and server-managed timestamps.
func ClusterProfilesSemanticallyEqual(existing, candidate *v1.ClusterProfile) bool {
	if existing == nil || candidate == nil || existing.Metadata == nil || candidate.Metadata == nil {
		return existing == candidate
	}

	return existing.APIVersion == candidate.APIVersion &&
		existing.Kind == candidate.Kind &&
		existing.Metadata.Name == candidate.Metadata.Name &&
		existing.Metadata.Workspace == candidate.Metadata.Workspace &&
		maps.Equal(existing.Metadata.Labels, candidate.Metadata.Labels) &&
		maps.Equal(existing.Metadata.Annotations, candidate.Metadata.Annotations) &&
		reflect.DeepEqual(existing.Spec, candidate.Spec)
}

func cloneReleaseInfo(info *v1.ReleaseInfo) *v1.ReleaseInfo {
	if info == nil {
		return nil
	}

	copy := *info
	copy.Metadata = cloneMetadata(info.Metadata)

	if info.Spec != nil {
		spec := *info.Spec
		spec.CompatibleClusterBaselines = append([]string(nil), info.Spec.CompatibleClusterBaselines...)
		copy.Spec = &spec
	}

	return &copy
}

func cloneClusterProfile(profile *v1.ClusterProfile) *v1.ClusterProfile {
	if profile == nil {
		return nil
	}

	copy := *profile
	copy.Metadata = cloneMetadata(profile.Metadata)

	if profile.Spec != nil {
		spec := *profile.Spec
		spec.Components = cloneClusterProfileComponents(profile.Spec.Components)
		copy.Spec = &spec
	}

	return &copy
}

func cloneClusterProfileComponents(values map[string]v1.ClusterProfileComponents) map[string]v1.ClusterProfileComponents {
	if values == nil {
		return nil
	}

	copy := make(map[string]v1.ClusterProfileComponents, len(values))
	for clusterType, components := range values {
		copy[clusterType] = components
	}

	return copy
}

func cloneMetadata(metadata *v1.Metadata) *v1.Metadata {
	if metadata == nil {
		return nil
	}

	copy := *metadata
	copy.Labels = cloneStringMap(metadata.Labels)
	copy.Annotations = cloneStringMap(metadata.Annotations)

	return &copy
}

func cloneStringMap(values map[string]string) map[string]string {
	if values == nil {
		return nil
	}

	copy := make(map[string]string, len(values))
	for key, value := range values {
		copy[key] = value
	}

	return copy
}
