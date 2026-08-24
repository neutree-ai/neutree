package releaseprofile

import (
	"fmt"
	"sort"
	"strings"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	// ComponentRayRuntime identifies the SSH ray_runtime component.
	ComponentRayRuntime = "ray_runtime"
	// ComponentKubernetesRuntime identifies the Kubernetes runtime component.
	ComponentKubernetesRuntime = "kubernetes_runtime"
	// ComponentRouter identifies the Kubernetes router component.
	ComponentRouter = "router"
	// ComponentNodeAgent identifies the node agent component.
	ComponentNodeAgent = "node_agent"
	// ComponentNodeExporter identifies the node exporter component.
	ComponentNodeExporter = "node_exporter"
	// ComponentVMAgent identifies the VictoriaMetrics agent component.
	ComponentVMAgent = "vmagent"
	// ComponentKubeStateMetrics identifies the kube-state-metrics component.
	ComponentKubeStateMetrics = "kube_state_metrics"

	builtinNodeExporterImage     = "quay.io/prometheus/node-exporter"
	builtinNodeExporterTag       = "v1.8.2"
	builtinVMAgentImage          = "victoriametrics/vmagent"
	builtinVMAgentTag            = "v1.115.0"
	builtinKubeStateMetricsImage = "registry.k8s.io/kube-state-metrics/kube-state-metrics"
	builtinKubeStateMetricsTag   = "v2.15.0"

	rayRuntimeComponent        = ComponentRayRuntime
	kubernetesRuntimeComponent = ComponentKubernetesRuntime
	routerComponent            = ComponentRouter
	nodeAgentComponent         = ComponentNodeAgent
	nodeExporterComponent      = ComponentNodeExporter
	vmagentComponent           = ComponentVMAgent
	kubeStateMetricsComponent  = ComponentKubeStateMetrics
)

// CatalogSpec is the immutable input used to construct a release catalog.
// Catalog copies every supplied value, so callers may safely reuse the input
// after NewCatalog returns.
type CatalogSpec struct {
	CurrentReleaseInfoBaseline string
	DefaultClusterVersion      string
	CompatibleClusterBaselines []string
	ClusterProfiles            []*v1.ClusterProfile
	ArtifactRules              []ArtifactRule
}

// ArtifactRule describes package-only material for one Cluster type and
// accelerator variant. Profile component matrices remain accelerator-neutral.
type ArtifactRule struct {
	ClusterType  string
	Accelerator  string
	Replacements []ComponentReplacement
	ExtraImages  []v1.ImageRef
}

// ComponentReplacement replaces one component reference while rendering a
// package image list. Tag and TagSuffix are mutually exclusive; an empty Image
// keeps the image name from the ClusterProfile component.
type ComponentReplacement struct {
	Component string
	Image     string
	Tag       string
	TagSuffix string
}

// Catalog owns release policy, exact component Profiles, and package-only
// artifact rules. Its fields are private so a constructed Catalog is immutable.
type Catalog struct {
	spec CatalogSpec
}

type builtinProfileMaterial struct {
	runtimeTag   string
	routerTag    string
	nodeAgentTag string
}

var builtinProfileMaterials = map[string]builtinProfileMaterial{
	"v1.1.0": {
		runtimeTag:   "v1.1.0",
		routerTag:    "v1.1.0",
		nodeAgentTag: "v1.1.0-alpha.8",
	},
	"v1.1.1": {
		runtimeTag:   "v1.1.1",
		routerTag:    "v1.1.1",
		nodeAgentTag: "v1.1.0-rc.1",
	},
	"v1.2.0": {
		runtimeTag:   "v1.1.1",
		routerTag:    "v1.1.1",
		nodeAgentTag: "v1.1.0-rc.1",
	},
}

var builtinProfileVersions = []string{"v1.1.0", "v1.1.1", "v1.2.0"}

var defaultCatalogState = struct {
	mu       sync.Mutex
	catalog  *Catalog
	consumed bool
	injected bool
}{catalog: builtinCatalog()}

// BuiltinCatalog returns an independent copy of the default release catalog.
// It does not consume the process-wide default and can be used as the source
// for an edition-specific Catalog passed to InjectCatalog.
func BuiltinCatalog() *Catalog {
	return cloneCatalog(builtinCatalog())
}

// NewCatalog validates and freezes a release catalog.
func NewCatalog(spec CatalogSpec) (*Catalog, error) {
	catalog := &Catalog{spec: cloneCatalogSpec(spec)}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}

	return catalog, nil
}

// Spec returns a deep copy of this Catalog's input. It is intended for
// constructing an edition-specific Catalog without exposing mutable internals.
func (catalog *Catalog) Spec() CatalogSpec {
	if catalog == nil {
		return CatalogSpec{}
	}

	return cloneCatalogSpec(catalog.spec)
}

// InjectCatalog replaces the process default before Core creates its first
// release-profile Builder. An injected catalog must retain the built-in version
// eligibility set so the shared CLI preflight contract remains valid.
func InjectCatalog(catalog *Catalog) error {
	if catalog == nil {
		return fmt.Errorf("release profile catalog is required")
	}

	if err := validateCatalog(catalog); err != nil {
		return fmt.Errorf("invalid injected release profile catalog: %w", err)
	}

	if err := validateCatalogEligibility(builtinCatalog(), catalog); err != nil {
		return err
	}

	defaultCatalogState.mu.Lock()
	defer defaultCatalogState.mu.Unlock()

	if defaultCatalogState.consumed {
		return fmt.Errorf("release profile catalog was already consumed")
	}

	if defaultCatalogState.injected {
		return fmt.Errorf("release profile catalog was already injected")
	}

	defaultCatalogState.catalog = cloneCatalog(catalog)
	defaultCatalogState.injected = true

	return nil
}

func defaultCatalog() *Catalog {
	defaultCatalogState.mu.Lock()
	defer defaultCatalogState.mu.Unlock()

	defaultCatalogState.consumed = true

	return cloneCatalog(defaultCatalogState.catalog)
}

func builtinCatalog() *Catalog {
	profiles := make([]*v1.ClusterProfile, 0, len(builtinProfileVersions))
	for _, version := range builtinProfileVersions {
		profiles = append(profiles, builtinClusterProfile(version))
	}

	catalog, err := NewCatalog(CatalogSpec{
		CurrentReleaseInfoBaseline: "v1.2.0",
		DefaultClusterVersion:      "v1.2.0",
		CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		ClusterProfiles:            profiles,
		ArtifactRules: []ArtifactRule{{
			ClusterType: v1.SSHClusterType,
			Accelerator: "amd_gpu",
			Replacements: []ComponentReplacement{{
				Component: rayRuntimeComponent,
				TagSuffix: "-rocm",
			}},
		}},
	})
	if err != nil {
		panic(fmt.Sprintf("invalid built-in release profile catalog: %v", err))
	}

	return catalog
}

func builtinClusterProfile(clusterVersion string) *v1.ClusterProfile {
	material, found := builtinProfileMaterials[clusterVersion]
	if !found {
		panic(fmt.Sprintf("missing built-in cluster profile material for %q", clusterVersion))
	}

	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: clusterVersion},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: material.runtimeTag},
				NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
				NodeExporter: v1.ImageRef{Image: builtinNodeExporterImage, Tag: builtinNodeExporterTag},
				VMAgent:      v1.ImageRef{Image: builtinVMAgentImage, Tag: builtinVMAgentTag},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: material.runtimeTag},
				Router:            v1.ImageRef{Image: "neutree/router", Tag: material.routerTag},
				NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
				NodeExporter:      v1.ImageRef{Image: builtinNodeExporterImage, Tag: builtinNodeExporterTag},
				VMAgent:           v1.ImageRef{Image: builtinVMAgentImage, Tag: builtinVMAgentTag},
				KubeStateMetrics:  v1.ImageRef{Image: builtinKubeStateMetricsImage, Tag: builtinKubeStateMetricsTag},
			},
		}},
	}
}

func validateCatalog(catalog *Catalog) error {
	if catalog == nil {
		return fmt.Errorf("release profile catalog is required")
	}

	info, err := catalog.buildReleaseInfo(catalog.spec.CurrentReleaseInfoBaseline)
	if err != nil {
		return err
	}

	if err := validateCurrentReleaseInfoBuilderOutput(catalog.spec.CurrentReleaseInfoBaseline, info); err != nil {
		return err
	}

	if err := validateCurrentClusterProfileCatalog(info, catalog.spec.ClusterProfiles); err != nil {
		return err
	}

	return validateArtifactRules(catalog.spec.ArtifactRules)
}

func validateCatalogEligibility(builtin, candidate *Catalog) error {
	builtinSpec := builtin.Spec()
	candidateSpec := candidate.Spec()

	if builtinSpec.DefaultClusterVersion != candidateSpec.DefaultClusterVersion {
		return fmt.Errorf("injected release profile catalog default cluster version differs from BuiltinCatalog")
	}

	if !sameStringSet(builtinSpec.CompatibleClusterBaselines, candidateSpec.CompatibleClusterBaselines) {
		return fmt.Errorf("injected release profile catalog compatible cluster baselines differ from BuiltinCatalog")
	}

	if !sameStringSet(profileNamesForCatalog(builtinSpec.ClusterProfiles), profileNamesForCatalog(candidateSpec.ClusterProfiles)) {
		return fmt.Errorf("injected release profile catalog eligible cluster profile versions differ from BuiltinCatalog")
	}

	return nil
}

func (catalog *Catalog) buildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	if err := catalog.validateBaseline(baseline); err != nil {
		return nil, err
	}

	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      catalog.spec.DefaultClusterVersion,
			CompatibleClusterBaselines: append([]string(nil), catalog.spec.CompatibleClusterBaselines...),
		},
	}, nil
}

func (catalog *Catalog) buildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error) {
	if err := catalog.validateBaseline(baseline); err != nil {
		return nil, err
	}

	profiles := make([]*v1.ClusterProfile, 0, len(catalog.spec.ClusterProfiles))
	for _, profile := range catalog.spec.ClusterProfiles {
		profiles = append(profiles, cloneClusterProfile(profile))
	}

	return profiles, nil
}

func (catalog *Catalog) buildPackageImages(clusterVersion, clusterType, accelerator string) ([]v1.ImageRef, error) {
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	if _, err := parseExactVPrefixedSemVer(clusterVersion); err != nil {
		return nil, fmt.Errorf("invalid cluster version %q: %w", clusterVersion, err)
	}

	profile := catalog.clusterProfile(clusterVersion)
	if profile == nil || profile.Spec == nil {
		return nil, fmt.Errorf("cluster profile %s not found", clusterVersion)
	}

	components, found := profile.Spec.ComponentsFor(clusterType)
	if !found {
		return nil, fmt.Errorf("cluster profile %s has no %s component matrix", clusterVersion, clusterType)
	}

	images, err := componentImages(clusterType, components)
	if err != nil {
		return nil, err
	}

	if rule := catalog.artifactRule(clusterType, strings.TrimSpace(accelerator)); rule != nil {
		if err := applyArtifactRule(images, *rule); err != nil {
			return nil, err
		}

		images = append(images, rule.ExtraImages...)
	}

	return dedupeImageRefs(images), nil
}

func (catalog *Catalog) validateBaseline(baseline string) error {
	if _, err := NormalizeControlPlaneRelease(baseline); err != nil {
		return err
	}

	requested, err := parseExactVPrefixedSemVer(baseline)
	if err != nil {
		return err
	}

	current, err := parseExactVPrefixedSemVer(catalog.spec.CurrentReleaseInfoBaseline)
	if err != nil {
		return fmt.Errorf("invalid catalog baseline %q: %w", catalog.spec.CurrentReleaseInfoBaseline, err)
	}

	if requested.Major() != current.Major() || requested.Minor() != current.Minor() {
		return fmt.Errorf("release baseline %q is not supported by catalog", baseline)
	}

	return nil
}

func (catalog *Catalog) clusterProfile(version string) *v1.ClusterProfile {
	for _, profile := range catalog.spec.ClusterProfiles {
		if profile.GetName() == version {
			return cloneClusterProfile(profile)
		}
	}

	return nil
}

func (catalog *Catalog) artifactRule(clusterType, accelerator string) *ArtifactRule {
	if accelerator == "" {
		return nil
	}

	for index := range catalog.spec.ArtifactRules {
		rule := &catalog.spec.ArtifactRules[index]
		if rule.ClusterType == clusterType && rule.Accelerator == accelerator {
			copy := cloneArtifactRule(*rule)
			return &copy
		}
	}

	return nil
}

func (catalog *Catalog) packageAccelerators(clusterType string) []string {
	if catalog == nil {
		return nil
	}

	accelerators := make([]string, 0, len(catalog.spec.ArtifactRules))

	for _, rule := range catalog.spec.ArtifactRules {
		if rule.ClusterType == clusterType {
			accelerators = append(accelerators, rule.Accelerator)
		}
	}

	sort.Strings(accelerators)

	return accelerators
}

func componentImages(clusterType string, components v1.ClusterProfileComponents) ([]v1.ImageRef, error) {
	switch clusterType {
	case v1.SSHClusterType:
		return []v1.ImageRef{components.RayRuntime, components.NodeAgent, components.NodeExporter, components.VMAgent}, nil
	case v1.KubernetesClusterType:
		return []v1.ImageRef{
			components.KubernetesRuntime,
			components.Router,
			components.NodeAgent,
			components.NodeExporter,
			components.VMAgent,
			components.KubeStateMetrics,
		}, nil
	default:
		return nil, fmt.Errorf("unsupported cluster profile type %q", clusterType)
	}
}

func componentImageIndexes(clusterType string) map[string]int {
	switch clusterType {
	case v1.SSHClusterType:
		return map[string]int{
			rayRuntimeComponent:   0,
			nodeAgentComponent:    1,
			nodeExporterComponent: 2,
			vmagentComponent:      3,
		}
	case v1.KubernetesClusterType:
		return map[string]int{
			kubernetesRuntimeComponent: 0,
			routerComponent:            1,
			nodeAgentComponent:         2,
			nodeExporterComponent:      3,
			vmagentComponent:           4,
			kubeStateMetricsComponent:  5,
		}
	default:
		return nil
	}
}

func applyArtifactRule(images []v1.ImageRef, rule ArtifactRule) error {
	indexes := componentImageIndexes(rule.ClusterType)
	for _, replacement := range rule.Replacements {
		index, found := indexes[replacement.Component]
		if !found {
			return fmt.Errorf("artifact rule references unsupported %s component %q", rule.ClusterType, replacement.Component)
		}

		if replacement.Image != "" {
			images[index].Image = replacement.Image
		}

		if replacement.Tag != "" {
			images[index].Tag = replacement.Tag
		}

		if replacement.TagSuffix != "" {
			images[index].Tag += replacement.TagSuffix
		}
	}

	return nil
}

func validateArtifactRules(rules []ArtifactRule) error {
	seen := make(map[string]struct{}, len(rules))

	for _, rule := range rules {
		if !v1.IsSupportedClusterType(rule.ClusterType) {
			return fmt.Errorf("artifact rule has unsupported cluster type %q", rule.ClusterType)
		}

		if strings.TrimSpace(rule.Accelerator) == "" || strings.TrimSpace(rule.Accelerator) != rule.Accelerator {
			return fmt.Errorf("artifact rule accelerator is required")
		}

		key := rule.ClusterType + "\x00" + rule.Accelerator
		if _, found := seen[key]; found {
			return fmt.Errorf("duplicate artifact rule for %s/%s", rule.ClusterType, rule.Accelerator)
		}

		seen[key] = struct{}{}

		components := componentImageIndexes(rule.ClusterType)
		seenComponents := make(map[string]struct{}, len(rule.Replacements))

		for _, replacement := range rule.Replacements {
			if _, found := components[replacement.Component]; !found {
				return fmt.Errorf("artifact rule has unsupported %s component %q", rule.ClusterType, replacement.Component)
			}

			if _, found := seenComponents[replacement.Component]; found {
				return fmt.Errorf("artifact rule duplicates %s component %q", rule.ClusterType, replacement.Component)
			}

			seenComponents[replacement.Component] = struct{}{}

			if replacement.Image == "" && replacement.Tag == "" && replacement.TagSuffix == "" {
				return fmt.Errorf("artifact rule replacement for %s is empty", replacement.Component)
			}

			if replacement.Tag != "" && replacement.TagSuffix != "" {
				return fmt.Errorf("artifact rule replacement for %s cannot set tag and tag suffix", replacement.Component)
			}
		}

		for _, image := range rule.ExtraImages {
			if strings.TrimSpace(image.Image) == "" || strings.TrimSpace(image.Tag) == "" {
				return fmt.Errorf("artifact rule extra image requires image and tag")
			}
		}
	}

	return nil
}

func cloneCatalog(catalog *Catalog) *Catalog {
	if catalog == nil {
		return nil
	}

	copy, err := NewCatalog(catalog.Spec())
	if err != nil {
		panic(fmt.Sprintf("clone release profile catalog: %v", err))
	}

	return copy
}

func cloneCatalogSpec(spec CatalogSpec) CatalogSpec {
	copy := CatalogSpec{
		CurrentReleaseInfoBaseline: spec.CurrentReleaseInfoBaseline,
		DefaultClusterVersion:      spec.DefaultClusterVersion,
		CompatibleClusterBaselines: append([]string(nil), spec.CompatibleClusterBaselines...),
		ClusterProfiles:            make([]*v1.ClusterProfile, 0, len(spec.ClusterProfiles)),
		ArtifactRules:              make([]ArtifactRule, 0, len(spec.ArtifactRules)),
	}

	for _, profile := range spec.ClusterProfiles {
		copy.ClusterProfiles = append(copy.ClusterProfiles, cloneClusterProfile(profile))
	}

	for _, rule := range spec.ArtifactRules {
		copy.ArtifactRules = append(copy.ArtifactRules, cloneArtifactRule(rule))
	}

	return copy
}

func cloneArtifactRule(rule ArtifactRule) ArtifactRule {
	return ArtifactRule{
		ClusterType:  rule.ClusterType,
		Accelerator:  rule.Accelerator,
		Replacements: append([]ComponentReplacement(nil), rule.Replacements...),
		ExtraImages:  append([]v1.ImageRef(nil), rule.ExtraImages...),
	}
}

func dedupeImageRefs(images []v1.ImageRef) []v1.ImageRef {
	seen := make(map[string]struct{}, len(images))
	result := make([]v1.ImageRef, 0, len(images))

	for _, image := range images {
		key := image.Image + ":" + image.Tag
		if _, found := seen[key]; found {
			continue
		}

		seen[key] = struct{}{}

		result = append(result, image)
	}

	return result
}

func profileNamesForCatalog(profiles []*v1.ClusterProfile) []string {
	names := make([]string, 0, len(profiles))

	for _, profile := range profiles {
		if profile != nil {
			names = append(names, profile.GetName())
		}
	}

	sort.Strings(names)

	return names
}

func sameStringSet(left, right []string) bool {
	if len(left) != len(right) {
		return false
	}

	leftSet := make(map[string]struct{}, len(left))
	for _, value := range left {
		leftSet[value] = struct{}{}
	}

	if len(leftSet) != len(left) {
		return false
	}

	for _, value := range right {
		if _, found := leftSet[value]; !found {
			return false
		}
	}

	return true
}
