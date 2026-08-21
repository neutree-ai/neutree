package releaseprofile

import (
	"fmt"
	"strings"

	"github.com/Masterminds/semver/v3"

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
	"v1.2": {
		runtimeTag:   "v1.1.1",
		routerTag:    "v1.1.1",
		nodeAgentTag: "v1.1.0-rc.1",
	},
}

// CommunityClusterProfile resolves the component material for one exact
// Cluster version and family. The catalog is shared by Core startup seeding
// and package construction so they cannot publish divergent material.
func CommunityClusterProfile(clusterVersion, clusterType string) (*v1.ClusterProfile, error) {
	if !v1.IsSupportedClusterType(clusterType) {
		return nil, fmt.Errorf("unsupported cluster profile type %q", clusterType)
	}

	material, err := communityProfileMaterialForVersion(clusterVersion)
	if err != nil {
		return nil, err
	}

	profile := &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: clusterVersion},
		Spec: &v1.ClusterProfileSpec{
			ClusterType: clusterType,
		},
	}

	switch clusterType {
	case v1.SSHClusterType:
		profile.Spec.Components = v1.ClusterProfileComponents{
			RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: material.runtimeTag},
			NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
			NodeExporter: v1.ImageRef{Image: communityNodeExporterImage, Tag: communityNodeExporterTag},
			VMAgent:      v1.ImageRef{Image: communityVMAgentImage, Tag: communityVMAgentTag},
		}
	case v1.KubernetesClusterType:
		profile.Spec.Components = v1.ClusterProfileComponents{
			KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: material.runtimeTag},
			Router:            v1.ImageRef{Image: "neutree/router", Tag: material.routerTag},
			NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: material.nodeAgentTag},
			NodeExporter:      v1.ImageRef{Image: communityNodeExporterImage, Tag: communityNodeExporterTag},
			VMAgent:           v1.ImageRef{Image: communityVMAgentImage, Tag: communityVMAgentTag},
			KubeStateMetrics:  v1.ImageRef{Image: communityKubeStateMetricsImage, Tag: communityKubeStateMetricsTag},
		}
	}

	return profile, nil
}

func communityProfileMaterialForVersion(clusterVersion string) (communityProfileMaterial, error) {
	if material, found := communityProfileMaterials[clusterVersion]; found {
		return material, nil
	}

	versionText := strings.TrimPrefix(strings.TrimSpace(clusterVersion), "v")

	version, err := semver.StrictNewVersion(versionText)
	if err != nil {
		return communityProfileMaterial{}, fmt.Errorf("unsupported community cluster profile version %q", clusterVersion)
	}

	if version.Major() == 1 && version.Minor() == 2 {
		return communityProfileMaterials["v1.2"], nil
	}

	return communityProfileMaterial{}, fmt.Errorf("unsupported community cluster profile version %q", clusterVersion)
}
