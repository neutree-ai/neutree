package releaseprofile

import (
	"fmt"
	"sort"
	"sync"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	builtinNodeExporterImage     = "quay.io/prometheus/node-exporter"
	builtinNodeExporterTag       = "v1.8.2"
	builtinVMAgentImage          = "victoriametrics/vmagent"
	builtinVMAgentTag            = "v1.115.0"
	builtinKubeStateMetricsImage = "registry.k8s.io/kube-state-metrics/kube-state-metrics"
	builtinKubeStateMetricsTag   = "v2.15.0"
)

// CatalogSpec is the immutable source for the current control-plane policy and
// exact ClusterProfile matrices. A Catalog copies all inputs on construction.
type CatalogSpec struct {
	CurrentReleaseInfoBaseline string
	DefaultClusterVersion      string
	CompatibleClusterBaselines []string
	ClusterProfiles            []*v1.ClusterProfile
}

// Catalog owns one release policy and its exact component Profiles.
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
}{}

// BuiltinCatalog returns an independent copy of the Core default Catalog. The
// name deliberately describes behavior rather than a product edition.
func BuiltinCatalog() *Catalog {
	return builtinCatalog()
}

// NewCatalog validates and freezes a release catalog.
func NewCatalog(spec CatalogSpec) (*Catalog, error) {
	catalog := &Catalog{spec: cloneCatalogSpec(spec)}
	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}

	return catalog, nil
}

// Spec returns a defensive copy suitable for creating a derived Catalog.
func (catalog *Catalog) Spec() CatalogSpec {
	if catalog == nil {
		return CatalogSpec{}
	}

	return cloneCatalogSpec(catalog.spec)
}

// InjectCatalog replaces the process default before the first default Builder
// is created. Enterprise uses this to supply edition-specific image material
// while retaining the same version eligibility as BuiltinCatalog.
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

	if defaultCatalogState.catalog == nil {
		defaultCatalogState.catalog = builtinCatalog()
	}

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

	if err := ValidateReleaseInfo(info); err != nil {
		return err
	}

	profiles, err := catalog.buildClusterProfiles(catalog.spec.CurrentReleaseInfoBaseline)
	if err != nil {
		return err
	}

	if len(profiles) == 0 {
		return fmt.Errorf("cluster profile catalog is empty")
	}

	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if err := ValidateProfileEligibility(info, profile); err != nil {
			return err
		}

		name := profile.GetName()
		if _, found := seen[name]; found {
			return fmt.Errorf("duplicate cluster profile %q", name)
		}

		seen[name] = struct{}{}
	}

	if _, found := seen[info.Spec.DefaultClusterVersion]; !found {
		return fmt.Errorf("cluster profile catalog is missing default cluster version %q", info.Spec.DefaultClusterVersion)
	}

	return nil
}

func validateCatalogEligibility(builtin, candidate *Catalog) error {
	builtinSpec := builtin.Spec()
	candidateSpec := candidate.Spec()

	if builtinSpec.CurrentReleaseInfoBaseline != candidateSpec.CurrentReleaseInfoBaseline {
		return fmt.Errorf("injected release profile catalog current release baseline differs from BuiltinCatalog")
	}

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
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	if err := catalog.validateBaseline(baseline); err != nil {
		return nil, err
	}

	profiles := make([]*v1.ClusterProfile, 0, len(catalog.spec.ClusterProfiles))
	for _, profile := range catalog.spec.ClusterProfiles {
		profiles = append(profiles, cloneClusterProfile(profile))
	}

	return profiles, nil
}

func (catalog *Catalog) validateBaseline(baseline string) error {
	if _, err := NormalizeControlPlaneRelease(baseline); err != nil {
		return err
	}

	if baseline != catalog.spec.CurrentReleaseInfoBaseline {
		return fmt.Errorf("release baseline %q is not supported by catalog", baseline)
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
	}

	for _, profile := range spec.ClusterProfiles {
		copy.ClusterProfiles = append(copy.ClusterProfiles, cloneClusterProfile(profile))
	}

	return copy
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

	values := make(map[string]struct{}, len(left))
	for _, value := range left {
		if _, found := values[value]; found {
			return false
		}

		values[value] = struct{}{}
	}

	for _, value := range right {
		if _, found := values[value]; !found {
			return false
		}
	}

	return true
}
