package releaseprofile

import (
	"fmt"
	"sort"
	"strings"

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

	builtinCurrentReleaseInfoBaseline = "v1.2.0"
)

// CatalogSpec is the immutable source for the current control-plane policy and
// exact ClusterProfile matrices. A Catalog copies all inputs on construction.
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

// Catalog owns one release policy, its exact component Profiles, and package
// artifact rules.
type Catalog struct {
	spec CatalogSpec
}

type builtinProfileMaterial struct {
	rayRuntime        v1.ImageRef
	kubernetesRuntime v1.ImageRef
	routerTag         string
	nodeAgentTag      string
}

var builtinProfileMaterials = map[string]builtinProfileMaterial{
	"v1.1.0": {
		rayRuntime:        v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.0"},
		kubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.0"},
		routerTag:         "v1.1.0",
		nodeAgentTag:      "v1.1.0-alpha.8",
	},
	"v1.1.1": {
		rayRuntime:        v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		kubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.1"},
		routerTag:         "v1.1.1",
		nodeAgentTag:      "v1.1.0-rc.1",
	},
	"v1.2.0": {
		rayRuntime:        v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.1.1"},
		kubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.1.1"},
		routerTag:         "v1.1.1",
		nodeAgentTag:      "v1.1.0-rc.1",
	},
}

var builtinProfileVersions = []string{"v1.1.0", "v1.1.1", "v1.2.0"}

var processCatalog = builtinCatalog()

// BuiltinCatalog returns an independent copy of the Core default Catalog. The
// name deliberately describes behavior rather than a product edition.
func BuiltinCatalog() *Catalog {
	return builtinCatalog()
}

// NewCatalog freezes a release catalog supplied by the Core build.
//
// The error return is retained for existing edition injection callers. Catalog
// contents are owned by the build and are intentionally not revalidated here.
func NewCatalog(spec CatalogSpec) (*Catalog, error) {
	return &Catalog{spec: cloneCatalogSpec(spec)}, nil
}

// Spec returns a defensive copy suitable for creating a derived Catalog.
func (catalog *Catalog) Spec() CatalogSpec {
	if catalog == nil {
		return CatalogSpec{}
	}

	return cloneCatalogSpec(catalog.spec)
}

// InjectCatalog replaces the process catalog during Core initialization.
// The caller owns injection order and supplies the Catalog.
func InjectCatalog(catalog *Catalog) {
	processCatalog = catalog
}

func defaultCatalog() *Catalog {
	return processCatalog
}

func builtinCatalog() *Catalog {
	profiles := make([]*v1.ClusterProfile, 0, len(builtinProfileVersions))
	for _, version := range builtinProfileVersions {
		profiles = append(profiles, builtinClusterProfile(version))
	}

	return &Catalog{spec: CatalogSpec{
		CurrentReleaseInfoBaseline: builtinCurrentReleaseInfoBaseline,
		DefaultClusterVersion:      "v1.2.0",
		CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		ClusterProfiles:            profiles,
		ArtifactRules: []ArtifactRule{{
			ClusterType: v1.SSHClusterType,
			Accelerator: "amd_gpu",
			Replacements: []ComponentReplacement{{
				Component: ComponentRayRuntime,
				TagSuffix: "-rocm",
			}},
		}},
	}}
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
				RayRuntime:   material.rayRuntime,
				NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
				NodeExporter: v1.ImageRef{Image: builtinNodeExporterImage, Tag: builtinNodeExporterTag},
				VMAgent:      v1.ImageRef{Image: builtinVMAgentImage, Tag: builtinVMAgentTag},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: material.kubernetesRuntime,
				Router:            v1.ImageRef{Image: "neutree/router", Tag: material.routerTag},
				NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
				NodeExporter:      v1.ImageRef{Image: builtinNodeExporterImage, Tag: builtinNodeExporterTag},
				VMAgent:           v1.ImageRef{Image: builtinVMAgentImage, Tag: builtinVMAgentTag},
				KubeStateMetrics:  v1.ImageRef{Image: builtinKubeStateMetricsImage, Tag: builtinKubeStateMetricsTag},
			},
		}},
	}
}

func (catalog *Catalog) buildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	if err := catalog.requireCurrentBaseline(baseline); err != nil {
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

	if err := catalog.requireCurrentBaseline(baseline); err != nil {
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
	}

	return images, nil
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
			ComponentRayRuntime:   0,
			ComponentNodeAgent:    1,
			ComponentNodeExporter: 2,
			ComponentVMAgent:      3,
		}
	case v1.KubernetesClusterType:
		return map[string]int{
			ComponentKubernetesRuntime: 0,
			ComponentRouter:            1,
			ComponentNodeAgent:         2,
			ComponentNodeExporter:      3,
			ComponentVMAgent:           4,
			ComponentKubeStateMetrics:  5,
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

func (catalog *Catalog) requireCurrentBaseline(baseline string) error {
	if baseline != catalog.spec.CurrentReleaseInfoBaseline {
		return fmt.Errorf("release baseline %q is not supported by catalog", baseline)
	}

	return nil
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
	}
}
