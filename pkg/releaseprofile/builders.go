package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ReleaseInfoBuilder builds the current ReleaseInfo candidate for a
// control-plane baseline. Core owns persistence of the returned object.
type ReleaseInfoBuilder interface {
	BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error)
}

// CurrentClusterProfileBuilder builds the current ClusterProfile candidate for
// a control-plane baseline. Core owns persistence of the returned object.
type CurrentClusterProfileBuilder interface {
	BuildClusterProfile(baseline string) (*v1.ClusterProfile, error)
}

// CommunityReleaseInfoBuilder provides the community release metadata for the
// currently supported control-plane baseline.
type CommunityReleaseInfoBuilder struct{}

// NewCommunityReleaseInfoBuilder returns the community ReleaseInfo builder.
func NewCommunityReleaseInfoBuilder() *CommunityReleaseInfoBuilder {
	return &CommunityReleaseInfoBuilder{}
}

// BuildReleaseInfo returns the semantic ReleaseInfo for a baseline.
func (builder *CommunityReleaseInfoBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if baseline != "v1.2.0" {
		return nil, fmt.Errorf("community release info baseline %q is not supported", baseline)
	}

	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
	}, nil
}

// CommunityClusterProfileBuilder provides the community ClusterProfile for
// the currently supported control-plane baseline.
type CommunityClusterProfileBuilder struct{}

// NewCommunityClusterProfileBuilder returns the community ClusterProfile builder.
func NewCommunityClusterProfileBuilder() *CommunityClusterProfileBuilder {
	return &CommunityClusterProfileBuilder{}
}

// BuildClusterProfile returns the typed component image profile for a baseline.
func (builder *CommunityClusterProfileBuilder) BuildClusterProfile(baseline string) (*v1.ClusterProfile, error) {
	if baseline != "v1.2.0" {
		return nil, fmt.Errorf("community cluster profile baseline %q is not supported", baseline)
	}

	return communityClusterProfile(
		baseline,
		"v1.1.1",
		"v1.1.1",
		"v1.1.0-rc.1",
	), nil
}

// CommunityHistoricalClusterProfile returns a historical community Profile.
// Core uses it only to seed missing initial v1.2 compatibility profiles.
func CommunityHistoricalClusterProfile(clusterVersion string) (*v1.ClusterProfile, error) {
	switch clusterVersion {
	case "v1.1.0":
		return communityClusterProfile(clusterVersion, "v1.1.0", "v1.1.0", "v1.1.0-alpha.8"), nil
	case "v1.1.1":
		return communityClusterProfile(clusterVersion, "v1.1.1", "v1.1.1", "v1.1.0-rc.1"), nil
	default:
		return nil, fmt.Errorf("community historical cluster profile %q is not supported", clusterVersion)
	}
}

func communityClusterProfile(baseline, rayRuntimeTag, routerTag, nodeAgentTag string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ClusterProfileSpec{Components: v1.ClusterProfileComponents{
			RayRuntime:       v1.ImageRef{Image: "neutree/neutree-serve", Tag: rayRuntimeTag},
			Router:           v1.ImageRef{Image: "neutree/router", Tag: routerTag},
			NodeAgent:        v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: nodeAgentTag},
			NodeExporter:     v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
			VMAgent:          v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			KubeStateMetrics: v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
		}},
	}
}
