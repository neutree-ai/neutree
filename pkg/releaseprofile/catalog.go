package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
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
}

// Catalog owns one release policy and its exact component Profiles.
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

// InjectCatalog replaces the process catalog during Core initialization.
// The caller owns injection order and supplies a validated Catalog.
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

	catalog, err := NewCatalog(CatalogSpec{
		CurrentReleaseInfoBaseline: builtinCurrentReleaseInfoBaseline,
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
