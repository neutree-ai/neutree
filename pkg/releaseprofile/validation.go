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

// ValidateReleaseInfo validates the policy fields before a caller writes the
// global resource. Persistence migrations intentionally do not duplicate these
// parameter checks.
func ValidateReleaseInfo(info *v1.ReleaseInfo) error {
	if info == nil || info.Metadata == nil || info.Spec == nil {
		return fmt.Errorf("release info, metadata, and spec are required")
	}

	if info.APIVersion != "v1" {
		return fmt.Errorf("release info api version must be v1")
	}

	if info.Kind != v1.ReleaseInfoKind {
		return fmt.Errorf("release info kind must be %s", v1.ReleaseInfoKind)
	}

	if info.Metadata.Workspace != "" {
		return fmt.Errorf("release info metadata.workspace must be empty")
	}

	if _, err := NormalizeControlPlaneRelease(info.Metadata.Name); err != nil {
		return err
	}

	if _, err := parseExactVPrefixedSemVer(info.Spec.DefaultClusterVersion); err != nil {
		if strings.TrimSpace(info.Spec.DefaultClusterVersion) == "" {
			return fmt.Errorf("default cluster version is required")
		}

		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	if len(info.Spec.CompatibleClusterBaselines) == 0 {
		return fmt.Errorf("compatible cluster baselines are required")
	}

	compatible := make(map[string]struct{}, len(info.Spec.CompatibleClusterBaselines))
	for _, baseline := range info.Spec.CompatibleClusterBaselines {
		if !compatibleClusterBaselinePattern.MatchString(baseline) {
			return fmt.Errorf("invalid compatible cluster baseline %q", baseline)
		}

		if _, found := compatible[baseline]; found {
			return fmt.Errorf("duplicate compatible cluster baseline %q", baseline)
		}

		compatible[baseline] = struct{}{}
	}

	defaultMinor, err := NormalizeClusterMinor(info.Spec.DefaultClusterVersion)
	if err != nil {
		return fmt.Errorf("invalid default cluster version %q: %w", info.Spec.DefaultClusterVersion, err)
	}

	if _, found := compatible[defaultMinor]; !found {
		return fmt.Errorf("default cluster version %q has incompatible baseline %q", info.Spec.DefaultClusterVersion, defaultMinor)
	}

	return nil
}

// ValidateClusterProfile validates the exact dual-type component matrix before
// a caller writes it. It is the domain counterpart to the intentionally small
// migration schema.
func ValidateClusterProfile(profile *v1.ClusterProfile) error {
	if profile == nil || profile.Metadata == nil || profile.Spec == nil {
		return fmt.Errorf("cluster profile, metadata, and spec are required")
	}

	if profile.APIVersion != "v1" {
		return fmt.Errorf("cluster profile api version must be v1")
	}

	if profile.Kind != v1.ClusterProfileKind {
		return fmt.Errorf("cluster profile kind must be %s", v1.ClusterProfileKind)
	}

	if profile.Metadata.Workspace != "" {
		return fmt.Errorf("cluster profile metadata.workspace must be empty")
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
			if strings.TrimSpace(component.ref.Image) == "" || strings.TrimSpace(component.ref.Image) != component.ref.Image {
				return fmt.Errorf("%s image is required", component.name)
			}

			if strings.TrimSpace(component.ref.Tag) == "" || strings.TrimSpace(component.ref.Tag) != component.ref.Tag {
				return fmt.Errorf("%s tag is required", component.name)
			}
		}
	}

	return nil
}

// ValidateProfileEligibility checks whether a complete Profile can be used by
// the supplied ReleaseInfo policy. It permits a later package import to add an
// exact profile that was not part of an older Core catalog, while enforcing the
// immutable compatibility boundary.
func ValidateProfileEligibility(info *v1.ReleaseInfo, profile *v1.ClusterProfile) error {
	if err := ValidateReleaseInfo(info); err != nil {
		return fmt.Errorf("invalid release info: %w", err)
	}

	if err := ValidateClusterProfile(profile); err != nil {
		return fmt.Errorf("invalid cluster profile: %w", err)
	}

	profileVersion, err := parseExactVPrefixedSemVer(profile.Metadata.Name)
	if err != nil {
		return err
	}

	defaultVersion, err := parseExactVPrefixedSemVer(info.Spec.DefaultClusterVersion)
	if err != nil {
		return err
	}

	if profileVersion.GreaterThan(defaultVersion) {
		return fmt.Errorf("cluster profile %q exceeds default cluster version %q", profile.Metadata.Name, info.Spec.DefaultClusterVersion)
	}

	minor, err := NormalizeClusterMinor(profile.Metadata.Name)
	if err != nil {
		return err
	}

	for _, compatible := range info.Spec.CompatibleClusterBaselines {
		if compatible == minor {
			return nil
		}
	}

	return fmt.Errorf("cluster profile %q has incompatible baseline %q", profile.Metadata.Name, minor)
}

// ClusterProfilesSemanticallyEqual compares caller-controlled protocol fields
// and deliberately ignores database identifiers and server timestamps.
func ClusterProfilesSemanticallyEqual(left, right *v1.ClusterProfile) bool {
	if left == nil || right == nil || left.Metadata == nil || right.Metadata == nil {
		return left == right
	}

	return left.APIVersion == right.APIVersion &&
		left.Kind == right.Kind &&
		left.Metadata.Name == right.Metadata.Name &&
		left.Metadata.Workspace == right.Metadata.Workspace &&
		maps.Equal(left.Metadata.Labels, right.Metadata.Labels) &&
		maps.Equal(left.Metadata.Annotations, right.Metadata.Annotations) &&
		reflect.DeepEqual(left.Spec, right.Spec)
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
