package releaseinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func TestSynchronizeCurrentBaselineCreatesCurrentPairAndMissingHistoricalProfiles(t *testing.T) {
	store := &currentBaselineMemoryStore{}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)
	require.NoError(t, err)
	require.Len(t, store.createdReleaseInfos, 1)
	require.Empty(t, store.updatedReleaseInfos)
	require.Len(t, store.createdClusterProfiles, 6)
	require.Empty(t, store.updatedClusterProfiles)

	assert.Equal(t, "v1.2.0", store.createdReleaseInfos[0].GetName())
	assert.Equal(t, []string{"v1.1", "v1.2"}, store.createdReleaseInfos[0].Spec.CompatibleClusterBaselines)
	assertSSHProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.0", v1.SSHClusterType), "v1.1.0", "v1.1.0-alpha.8")
	assertKubernetesProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.0", v1.KubernetesClusterType), "v1.1.0", "v1.1.0", "v1.1.0-alpha.8")
	assertSSHProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.1", v1.SSHClusterType), "v1.1.1", "v1.1.0-rc.1")
	assertKubernetesProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.1", v1.KubernetesClusterType), "v1.1.1", "v1.1.1", "v1.1.0-rc.1")
	assertSSHProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.2.0", v1.SSHClusterType), "v1.1.1", "v1.1.0-rc.1")
	assertKubernetesProfileTags(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.2.0", v1.KubernetesClusterType), "v1.1.1", "v1.1.1", "v1.1.0-rc.1")
}

func TestSynchronizeCurrentBaselineOverwritesOnlyCurrentPairAndPreservesExistingProfiles(t *testing.T) {
	existingHistorical := clusterProfileNamed("v1.1.1", v1.SSHClusterType, "preserve")
	existingPrerelease := clusterProfileNamed("v1.2.0-alpha.1", v1.KubernetesClusterType, "preserve-alpha")
	store := &currentBaselineMemoryStore{
		releaseInfos: []v1.ReleaseInfo{{ID: 11, Metadata: &v1.Metadata{Name: "v1.2.0"}, Spec: &v1.ReleaseInfoSpec{}}},
		clusterProfiles: []v1.ClusterProfile{
			{ID: 21, Metadata: &v1.Metadata{Name: "v1.2.0"}, Spec: &v1.ClusterProfileSpec{ClusterType: v1.SSHClusterType}},
			{ID: 22, Metadata: &v1.Metadata{Name: "v1.2.0"}, Spec: &v1.ClusterProfileSpec{ClusterType: v1.KubernetesClusterType}},
			*existingHistorical,
			*existingPrerelease,
		},
	}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)
	require.NoError(t, err)
	require.Empty(t, store.createdReleaseInfos)
	require.Len(t, store.updatedReleaseInfos, 1)
	assert.Equal(t, "11", store.updatedReleaseInfoIDs[0])
	require.Len(t, store.updatedClusterProfiles, 2)
	assert.ElementsMatch(t, []string{"21", "22"}, store.updatedClusterProfileIDs)
	require.Len(t, store.createdClusterProfiles, 3)
	assert.NotNil(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.0", v1.SSHClusterType))
	assert.NotNil(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.0", v1.KubernetesClusterType))
	assert.NotNil(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.1.1", v1.KubernetesClusterType))
	assert.Equal(t, "preserve", existingHistorical.Spec.Components.RayRuntime.Tag)
	assert.Equal(t, "preserve-alpha", existingPrerelease.Spec.Components.RayRuntime.Tag)
}

func TestSynchronizeCurrentBaselineDoesNotSeedHistoryForOtherBaseline(t *testing.T) {
	store := &currentBaselineMemoryStore{}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.3.0",
		releaseInfoBuilderFunc(func(baseline string) (*v1.ReleaseInfo, error) {
			return releaseInfoBuilderOutput(baseline, []string{"v1.3"}, nil), nil
		}),
		clusterProfileBuilderFunc(func(baseline, clusterType string) (*v1.ClusterProfile, error) {
			return clusterProfileBuilderOutput(baseline, clusterType, nil), nil
		}),
	)
	require.NoError(t, err)
	assert.Len(t, store.createdReleaseInfos, 1)
	assert.Len(t, store.createdClusterProfiles, 2)
	assert.NotNil(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.3.0", v1.SSHClusterType))
	assert.NotNil(t, findCreatedClusterProfile(t, store.createdClusterProfiles, "v1.3.0", v1.KubernetesClusterType))
}

func TestSynchronizeCurrentBaselineRejectsPrereleaseBeforeBuildersOrStore(t *testing.T) {
	store := &currentBaselineMemoryStore{}
	releaseBuilderCalled := false
	profileBuilderCalled := false

	err := SynchronizeCurrentBaseline(
		store,
		"v1.3.0-rc.1",
		releaseInfoBuilderFunc(func(baseline string) (*v1.ReleaseInfo, error) {
			releaseBuilderCalled = true
			return releaseInfoBuilderOutput(baseline, []string{"v1.3"}, nil), nil
		}),
		clusterProfileBuilderFunc(func(baseline, _ string) (*v1.ClusterProfile, error) {
			profileBuilderCalled = true
			return clusterProfileBuilderOutput(baseline, v1.SSHClusterType, nil), nil
		}),
	)
	require.ErrorContains(t, err, "stable release info baseline")
	assert.False(t, releaseBuilderCalled)
	assert.False(t, profileBuilderCalled)
	assert.Zero(t, store.listReleaseInfoCalls)
	assert.Zero(t, store.listClusterProfileCalls)
}

func TestSynchronizeCurrentBaselineRejectsBuilderOutputForAnotherBaseline(t *testing.T) {
	store := &currentBaselineMemoryStore{}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseInfoBuilderFunc(func(string) (*v1.ReleaseInfo, error) {
			return releaseInfoBuilderOutput("v1.1.0", []string{"v1.1"}, nil), nil
		}),
		clusterProfileBuilderFunc(func(baseline, clusterType string) (*v1.ClusterProfile, error) {
			return clusterProfileBuilderOutput(baseline, clusterType, nil), nil
		}),
	)
	require.ErrorContains(t, err, "release info builder output name")
	assert.Empty(t, store.createdReleaseInfos)
	assert.Empty(t, store.createdClusterProfiles)
}

func TestValidateCurrentReleaseInfoBuilderOutputRejectsInvalidStructure(t *testing.T) {
	testCases := []struct {
		name string
		info *v1.ReleaseInfo
		want string
	}{
		{
			name: "wrong api version",
			info: releaseInfoBuilderOutput("v1.2.0", []string{"v1.2"}, func(info *v1.ReleaseInfo) {
				info.APIVersion = "v2"
			}),
			want: "api version",
		},
		{
			name: "wrong kind",
			info: releaseInfoBuilderOutput("v1.2.0", []string{"v1.2"}, func(info *v1.ReleaseInfo) {
				info.Kind = "Other"
			}),
			want: "kind",
		},
		{
			name: "empty compatible baselines",
			info: releaseInfoBuilderOutput("v1.2.0", nil, nil),
			want: "compatible cluster baselines",
		},
		{
			name: "malformed compatible baseline",
			info: releaseInfoBuilderOutput("v1.2.0", []string{"v1.2.0"}, nil),
			want: "invalid compatible cluster baseline",
		},
		{
			name: "duplicate compatible baseline",
			info: releaseInfoBuilderOutput("v1.2.0", []string{"v1.1", "v1.1"}, nil),
			want: "duplicate compatible cluster baseline",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateCurrentReleaseInfoBuilderOutput("v1.2.0", testCase.info)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestValidateCurrentClusterProfileBuilderOutputRejectsInvalidStructure(t *testing.T) {
	testCases := []struct {
		name    string
		profile *v1.ClusterProfile
		want    string
	}{
		{
			name: "wrong api version",
			profile: clusterProfileBuilderOutput("v1.2.0", v1.SSHClusterType, func(profile *v1.ClusterProfile) {
				profile.APIVersion = "v2"
			}),
			want: "api version",
		},
		{
			name: "wrong kind",
			profile: clusterProfileBuilderOutput("v1.2.0", v1.SSHClusterType, func(profile *v1.ClusterProfile) {
				profile.Kind = "Other"
			}),
			want: "kind",
		},
		{
			name: "missing component image",
			profile: clusterProfileBuilderOutput("v1.2.0", v1.SSHClusterType, func(profile *v1.ClusterProfile) {
				profile.Spec.Components.RayRuntime.Image = ""
			}),
			want: "ray runtime image",
		},
		{
			name: "missing component tag",
			profile: clusterProfileBuilderOutput("v1.2.0", v1.KubernetesClusterType, func(profile *v1.ClusterProfile) {
				profile.Spec.Components.KubeStateMetrics.Tag = ""
			}),
			want: "kube state metrics tag",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateCurrentClusterProfileBuilderOutput("v1.2.0", testCase.profile.GetClusterType(), testCase.profile)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

type currentBaselineMemoryStore struct {
	releaseInfos    []v1.ReleaseInfo
	clusterProfiles []v1.ClusterProfile

	listReleaseInfoCalls    int
	listClusterProfileCalls int

	createdReleaseInfos      []*v1.ReleaseInfo
	updatedReleaseInfos      []*v1.ReleaseInfo
	updatedReleaseInfoIDs    []string
	createdClusterProfiles   []*v1.ClusterProfile
	updatedClusterProfiles   []*v1.ClusterProfile
	updatedClusterProfileIDs []string
}

func (store *currentBaselineMemoryStore) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	store.listReleaseInfoCalls++
	return store.releaseInfos, nil
}

func (store *currentBaselineMemoryStore) CreateReleaseInfo(info *v1.ReleaseInfo) error {
	store.createdReleaseInfos = append(store.createdReleaseInfos, info)
	return nil
}

func (store *currentBaselineMemoryStore) UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error {
	store.updatedReleaseInfoIDs = append(store.updatedReleaseInfoIDs, id)
	store.updatedReleaseInfos = append(store.updatedReleaseInfos, info)
	return nil
}

func (store *currentBaselineMemoryStore) ListClusterProfile() ([]v1.ClusterProfile, error) {
	store.listClusterProfileCalls++
	return store.clusterProfiles, nil
}

func (store *currentBaselineMemoryStore) CreateClusterProfile(profile *v1.ClusterProfile) error {
	store.createdClusterProfiles = append(store.createdClusterProfiles, profile)
	return nil
}

func (store *currentBaselineMemoryStore) UpdateClusterProfile(id string, profile *v1.ClusterProfile) error {
	store.updatedClusterProfileIDs = append(store.updatedClusterProfileIDs, id)
	store.updatedClusterProfiles = append(store.updatedClusterProfiles, profile)
	return nil
}

type releaseInfoBuilderFunc func(string) (*v1.ReleaseInfo, error)

func (builder releaseInfoBuilderFunc) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	return builder(baseline)
}

type clusterProfileBuilderFunc func(string, string) (*v1.ClusterProfile, error)

func (builder clusterProfileBuilderFunc) BuildClusterProfile(baseline, clusterType string) (*v1.ClusterProfile, error) {
	return builder(baseline, clusterType)
}

func clusterProfileNamed(name, clusterType, rayRuntimeTag string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.ClusterProfileSpec{ClusterType: clusterType, Components: v1.ClusterProfileComponents{
			RayRuntime: v1.ImageRef{Tag: rayRuntimeTag},
		}},
	}
}

func findCreatedClusterProfile(t *testing.T, profiles []*v1.ClusterProfile, name, clusterType string) *v1.ClusterProfile {
	t.Helper()
	for _, profile := range profiles {
		if profile.GetName() == name && profile.GetClusterType() == clusterType {
			return profile
		}
	}
	t.Fatalf("cluster profile %s/%s was not created", name, clusterType)
	return nil
}

func assertSSHProfileTags(t *testing.T, profile *v1.ClusterProfile, rayRuntime, nodeAgent string) {
	t.Helper()
	require.NotNil(t, profile.Spec)
	assert.Equal(t, v1.SSHClusterType, profile.GetClusterType())
	assert.Equal(t, rayRuntime, profile.Spec.Components.RayRuntime.Tag)
	assert.Equal(t, nodeAgent, profile.Spec.Components.NodeAgent.Tag)
	assert.Equal(t, "v1.8.2", profile.Spec.Components.NodeExporter.Tag)
	assert.Equal(t, "v1.115.0", profile.Spec.Components.VMAgent.Tag)
}

func assertKubernetesProfileTags(t *testing.T, profile *v1.ClusterProfile, runtime, router, nodeAgent string) {
	t.Helper()
	require.NotNil(t, profile.Spec)
	assert.Equal(t, v1.KubernetesClusterType, profile.GetClusterType())
	assert.Equal(t, runtime, profile.Spec.Components.KubernetesRuntime.Tag)
	assert.Equal(t, router, profile.Spec.Components.Router.Tag)
	assert.Equal(t, nodeAgent, profile.Spec.Components.NodeAgent.Tag)
	assert.Equal(t, "v1.8.2", profile.Spec.Components.NodeExporter.Tag)
	assert.Equal(t, "v1.115.0", profile.Spec.Components.VMAgent.Tag)
	assert.Equal(t, "v2.15.0", profile.Spec.Components.KubeStateMetrics.Tag)
}

func releaseInfoBuilderOutput(baseline string, compatibleClusterBaselines []string, mutate func(*v1.ReleaseInfo)) *v1.ReleaseInfo {
	info := &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			CompatibleClusterBaselines: compatibleClusterBaselines,
		},
	}
	if mutate != nil {
		mutate(info)
	}

	return info
}

func clusterProfileBuilderOutput(baseline, clusterType string, mutate func(*v1.ClusterProfile)) *v1.ClusterProfile {
	profile := &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: baseline},
		Spec: &v1.ClusterProfileSpec{ClusterType: clusterType, Components: v1.ClusterProfileComponents{
			RayRuntime:        v1.ImageRef{Image: "ray", Tag: "tag"},
			KubernetesRuntime: v1.ImageRef{Image: "kubernetes-runtime", Tag: "tag"},
			Router:            v1.ImageRef{Image: "router", Tag: "tag"},
			NodeAgent:         v1.ImageRef{Image: "node-agent", Tag: "tag"},
			NodeExporter:      v1.ImageRef{Image: "node-exporter", Tag: "tag"},
			VMAgent:           v1.ImageRef{Image: "vmagent", Tag: "tag"},
			KubeStateMetrics:  v1.ImageRef{Image: "kube-state-metrics", Tag: "tag"},
		}},
	}
	if mutate != nil {
		mutate(profile)
	}

	return profile
}
