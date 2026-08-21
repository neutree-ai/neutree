package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	communityNodeExporterImage     = "quay.io/prometheus/node-exporter"
	communityNodeExporterTag       = "v1.8.2"
	communityVMAgentImage          = "victoriametrics/vmagent"
	communityVMAgentTag            = "v1.115.0"
	communityKubeStateMetricsImage = "registry.k8s.io/kube-state-metrics/kube-state-metrics"
	communityKubeStateMetricsTag   = "v2.15.0"
)

type communityProfileMaterial struct {
	runtimeTag   string
	routerTag    string
	nodeAgentTag string
}

var communityProfileMaterials = map[string]communityProfileMaterial{
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

var communityClusterProfileVersions = []string{"v1.1.0", "v1.1.1", "v1.2.0"}

// CommunityClusterProfile resolves the component material for one exact
// Cluster version. The catalog is shared by Core startup seeding and package
// construction so they cannot publish divergent material.
func CommunityClusterProfile(clusterVersion string) (*v1.ClusterProfile, error) {
	material, err := communityProfileMaterialForVersion(clusterVersion)
	if err != nil {
		return nil, err
	}

	profile := &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: clusterVersion},
		Spec: &v1.ClusterProfileSpec{
			Components: map[string]v1.ClusterProfileComponents{
				v1.SSHClusterType: {
					RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: material.runtimeTag},
					NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
					NodeExporter: v1.ImageRef{Image: communityNodeExporterImage, Tag: communityNodeExporterTag},
					VMAgent:      v1.ImageRef{Image: communityVMAgentImage, Tag: communityVMAgentTag},
				},
				v1.KubernetesClusterType: {
					KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: material.runtimeTag},
					Router:            v1.ImageRef{Image: "neutree/router", Tag: material.routerTag},
					NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
					NodeExporter:      v1.ImageRef{Image: communityNodeExporterImage, Tag: communityNodeExporterTag},
					VMAgent:           v1.ImageRef{Image: communityVMAgentImage, Tag: communityVMAgentTag},
					KubeStateMetrics:  v1.ImageRef{Image: communityKubeStateMetricsImage, Tag: communityKubeStateMetricsTag},
				},
			},
		},
	}

	return profile, nil
}

// CommunityClusterProfiles returns every exact ClusterProfile carried by the
// current community release catalog.
func CommunityClusterProfiles() ([]*v1.ClusterProfile, error) {
	profiles := make([]*v1.ClusterProfile, 0, len(communityClusterProfileVersions))

	for _, version := range communityClusterProfileVersions {
		profile, err := CommunityClusterProfile(version)
		if err != nil {
			return nil, err
		}

		profiles = append(profiles, profile)
	}

	return profiles, nil
}

func communityProfileMaterialForVersion(clusterVersion string) (communityProfileMaterial, error) {
	if material, found := communityProfileMaterials[clusterVersion]; found {
		return material, nil
	}

	return communityProfileMaterial{}, fmt.Errorf("unsupported community cluster profile version %q", clusterVersion)
}
