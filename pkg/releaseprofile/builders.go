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

// CurrentReleaseInfoBaselineProvider supplies the stable baseline that a
// ReleaseInfoBuilder can initialize when a development build has no persisted
// ReleaseInfo yet. It remains separate from ReleaseInfoBuilder so existing
// edition-specific builders do not need to opt in unless they support startup
// bootstrap.
type CurrentReleaseInfoBaselineProvider interface {
	CurrentReleaseInfoBaseline() string
}

// CurrentClusterProfileBuilder builds the current ClusterProfile candidate for
// a control-plane baseline. Core owns persistence of the returned object.
type CurrentClusterProfileBuilder interface {
	BuildClusterProfile(baseline, clusterType string) (*v1.ClusterProfile, error)
}

// CommunityReleaseInfoBuilder provides the community release metadata for the
// currently supported control-plane baseline.
type CommunityReleaseInfoBuilder struct{}

// CurrentCommunityReleaseInfoBaseline is the stable baseline carried by the
// community control-plane build.
const CurrentCommunityReleaseInfoBaseline = "v1.2.0"

// NewCommunityReleaseInfoBuilder returns the community ReleaseInfo builder.
func NewCommunityReleaseInfoBuilder() *CommunityReleaseInfoBuilder {
	return &CommunityReleaseInfoBuilder{}
}

// CurrentReleaseInfoBaseline returns the baseline used to bootstrap a fresh
// database from a development control-plane build.
func (builder *CommunityReleaseInfoBuilder) CurrentReleaseInfoBaseline() string {
	return CurrentCommunityReleaseInfoBaseline
}

// BuildReleaseInfo returns the semantic ReleaseInfo for a baseline.
func (builder *CommunityReleaseInfoBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if baseline != CurrentCommunityReleaseInfoBaseline {
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

// BuildClusterProfile returns the typed component image profile for a baseline
// and cluster family.
func (builder *CommunityClusterProfileBuilder) BuildClusterProfile(baseline, clusterType string) (*v1.ClusterProfile, error) {
	if baseline != "v1.2.0" {
		return nil, fmt.Errorf("community cluster profile baseline %q is not supported", baseline)
	}

	return CommunityClusterProfile(baseline, clusterType)
}

// CommunityHistoricalClusterProfile returns a historical community Profile.
// Core uses it only to seed missing initial v1.2 compatibility profiles.
func CommunityHistoricalClusterProfile(clusterVersion, clusterType string) (*v1.ClusterProfile, error) {
	return CommunityClusterProfile(clusterVersion, clusterType)
}
