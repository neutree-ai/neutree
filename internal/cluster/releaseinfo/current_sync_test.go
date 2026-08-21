package releaseinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
)

func TestSynchronizeCurrentBaselineCreatesExactCatalogAndDefault(t *testing.T) {
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
	require.Len(t, store.createdClusterProfiles, 3)

	info := store.createdReleaseInfos[0]
	assert.Equal(t, "v1.2.0", info.GetName())
	assert.Equal(t, "v1.2.0", info.Spec.DefaultClusterVersion)
	assert.Equal(t, []string{"v1.1", "v1.2"}, info.Spec.CompatibleClusterBaselines)

	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, createdProfileNames(store.createdClusterProfiles))
	for _, profile := range store.createdClusterProfiles {
		assertCompleteProfile(t, profile)
	}
}

func TestSynchronizeCurrentBaselineUpdatesReleaseInfoAndLeavesIdenticalProfiles(t *testing.T) {
	profiles := []*v1.ClusterProfile{
		mustCommunityProfile(t, "v1.1.0"),
		mustCommunityProfile(t, "v1.1.1"),
		mustCommunityProfile(t, "v1.2.0"),
	}
	storedProfiles := make([]v1.ClusterProfile, 0, len(profiles))
	for index, profile := range profiles {
		profile.ID = index + 1
		profile.Metadata.CreationTimestamp = "2026-08-21T00:00:00Z"
		profile.Metadata.UpdateTimestamp = "2026-08-21T00:00:00Z"
		storedProfiles = append(storedProfiles, *profile)
	}

	store := &currentBaselineMemoryStore{
		releaseInfos: []v1.ReleaseInfo{{
			ID:       11,
			Metadata: &v1.Metadata{Name: "v1.2.0"},
			Spec: &v1.ReleaseInfoSpec{
				DefaultClusterVersion:      "v1.1.1",
				CompatibleClusterBaselines: []string{"v1.1"},
			},
		}},
		clusterProfiles: storedProfiles,
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
	assert.Equal(t, "v1.2.0", store.updatedReleaseInfos[0].Spec.DefaultClusterVersion)
	assert.Empty(t, store.createdClusterProfiles)
}

func TestSynchronizeCurrentBaselineRejectsProfileDriftBeforeWriting(t *testing.T) {
	drifted := mustCommunityProfile(t, "v1.2.0")
	ssh, found := drifted.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	ssh.RayRuntime.Tag = "drifted"
	drifted.Spec.Components[v1.SSHClusterType] = ssh
	drifted.ID = 12

	store := &currentBaselineMemoryStore{
		releaseInfos: []v1.ReleaseInfo{{
			ID:       11,
			Metadata: &v1.Metadata{Name: "v1.2.0"},
			Spec: &v1.ReleaseInfoSpec{
				DefaultClusterVersion:      "v1.1.1",
				CompatibleClusterBaselines: []string{"v1.1"},
			},
		}},
		clusterProfiles: []v1.ClusterProfile{*drifted},
	}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)
	require.ErrorContains(t, err, "cluster profile v1.2.0 content drift")
	assert.Empty(t, store.createdReleaseInfos)
	assert.Empty(t, store.updatedReleaseInfos)
	assert.Empty(t, store.createdClusterProfiles)
}

func TestSynchronizeCurrentBaselineCreatesOnlyMissingProfiles(t *testing.T) {
	first := mustCommunityProfile(t, "v1.1.0")
	second := mustCommunityProfile(t, "v1.1.1")
	first.ID = 1
	second.ID = 2
	store := &currentBaselineMemoryStore{clusterProfiles: []v1.ClusterProfile{*first, *second}}

	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)
	require.NoError(t, err)
	require.Len(t, store.createdClusterProfiles, 1)
	assert.Equal(t, "v1.2.0", store.createdClusterProfiles[0].GetName())
}

func TestSynchronizeCurrentBaselinePreservesPrereleaseReleaseIdentity(t *testing.T) {
	store := &currentBaselineMemoryStore{}
	baseline := "v1.3.0-alpha.1"

	err := SynchronizeCurrentBaseline(
		store,
		baseline,
		releaseInfoBuilderFunc(func(name string) (*v1.ReleaseInfo, error) {
			return releaseInfoBuilderOutput(name, baseline, []string{"v1.3"}), nil
		}),
		clusterProfileBuilderFunc(func(string) ([]*v1.ClusterProfile, error) {
			return []*v1.ClusterProfile{completeProfile(baseline)}, nil
		}),
	)
	require.NoError(t, err)
	require.Len(t, store.createdReleaseInfos, 1)
	assert.Equal(t, baseline, store.createdReleaseInfos[0].GetName())
	require.Len(t, store.createdClusterProfiles, 1)
	assert.Equal(t, baseline, store.createdClusterProfiles[0].GetName())
}

func TestValidateCurrentReleaseInfoBuilderOutputRejectsInvalidPolicy(t *testing.T) {
	testCases := []struct {
		name string
		info *v1.ReleaseInfo
		want string
	}{
		{
			name: "missing default cluster version",
			info: releaseInfoBuilderOutput("v1.2.0", "", []string{"v1.2"}),
			want: "default cluster version is required",
		},
		{
			name: "invalid default cluster version",
			info: releaseInfoBuilderOutput("v1.2.0", "v1.2", []string{"v1.2"}),
			want: "invalid default cluster version",
		},
		{
			name: "duplicate compatible baseline",
			info: releaseInfoBuilderOutput("v1.2.0", "v1.2.0", []string{"v1.2", "v1.2"}),
			want: "duplicate compatible cluster baseline",
		},
		{
			name: "default cluster version outside compatible baselines",
			info: releaseInfoBuilderOutput("v1.2.0", "v1.2.0", []string{"v1.1"}),
			want: "default cluster version \"v1.2.0\" has incompatible baseline \"v1.2\"",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateCurrentReleaseInfoBuilderOutput("v1.2.0", testCase.info)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestValidateCurrentClusterProfileCatalogRejectsEmptyCatalog(t *testing.T) {
	info := releaseInfoBuilderOutput("v1.2.0", "v1.2.0", []string{"v1.1", "v1.2"})

	err := validateCurrentClusterProfileCatalog(info, nil)

	assert.EqualError(t, err, "cluster profile catalog is empty")
}

func TestValidateCurrentClusterProfileCatalogRequiresDefaultProfile(t *testing.T) {
	info := releaseInfoBuilderOutput("v1.2.0", "v1.2.0", []string{"v1.1", "v1.2"})

	err := validateCurrentClusterProfileCatalog(info, []*v1.ClusterProfile{completeProfile("v1.1.1")})

	assert.EqualError(t, err, `cluster profile catalog is missing default cluster version "v1.2.0"`)
}

func TestValidateCurrentClusterProfileBuilderOutputRequiresCompleteMatrices(t *testing.T) {
	testCases := []struct {
		name    string
		profile *v1.ClusterProfile
		want    string
	}{
		{
			name: "missing kubernetes matrix",
			profile: func() *v1.ClusterProfile {
				profile := completeProfile("v1.2.0")
				delete(profile.Spec.Components, v1.KubernetesClusterType)
				return profile
			}(),
			want: "kubernetes component matrix is required",
		},
		{
			name: "unsupported matrix type",
			profile: func() *v1.ClusterProfile {
				profile := completeProfile("v1.2.0")
				profile.Spec.Components["docker"] = v1.ClusterProfileComponents{}
				return profile
			}(),
			want: "unsupported component matrix type",
		},
		{
			name: "missing required component tag",
			profile: func() *v1.ClusterProfile {
				profile := completeProfile("v1.2.0")
				components, found := profile.Spec.ComponentsFor(v1.KubernetesClusterType)
				require.True(t, found)
				components.KubeStateMetrics.Tag = ""
				profile.Spec.Components[v1.KubernetesClusterType] = components
				return profile
			}(),
			want: "kube state metrics tag",
		},
		{
			name: "workspace metadata",
			profile: func() *v1.ClusterProfile {
				profile := completeProfile("v1.2.0")
				profile.Metadata.Workspace = "default"
				return profile
			}(),
			want: "metadata.workspace must be empty",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateCurrentClusterProfileBuilderOutput(testCase.profile)
			require.ErrorContains(t, err, testCase.want)
		})
	}
}

func TestSynchronizeCurrentBaselineIgnoresServerTimestampsOnReplay(t *testing.T) {
	persisted := mustCommunityProfile(t, "v1.2.0")
	persisted.Metadata.CreationTimestamp = "2026-01-01T00:00:00Z"
	persisted.Metadata.UpdateTimestamp = "2026-01-02T00:00:00Z"
	persisted.Metadata.DeletionTimestamp = "2026-01-03T00:00:00Z"

	store := &currentBaselineMemoryStore{clusterProfiles: []v1.ClusterProfile{*persisted}}
	err := SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)

	require.NoError(t, err)
	assert.NotContains(t, createdProfileNames(store.createdClusterProfiles), "v1.2.0")
}

func TestSynchronizeCurrentBaselineTreatsEmptyMetadataMapsAsIdentical(t *testing.T) {
	profiles, err := releaseprofile.CommunityClusterProfiles()
	require.NoError(t, err)
	storedProfiles := make([]v1.ClusterProfile, 0, len(profiles))
	for _, profile := range profiles {
		profile.Metadata.Labels = map[string]string{}
		profile.Metadata.Annotations = map[string]string{}
		storedProfiles = append(storedProfiles, *profile)
	}

	store := &currentBaselineMemoryStore{clusterProfiles: storedProfiles}
	err = SynchronizeCurrentBaseline(
		store,
		"v1.2.0",
		releaseprofile.NewCommunityReleaseInfoBuilder(),
		releaseprofile.NewCommunityClusterProfileBuilder(),
	)

	require.NoError(t, err)
	assert.Empty(t, store.createdClusterProfiles)
}

type currentBaselineMemoryStore struct {
	releaseInfos    []v1.ReleaseInfo
	clusterProfiles []v1.ClusterProfile

	createdReleaseInfos    []*v1.ReleaseInfo
	updatedReleaseInfos    []*v1.ReleaseInfo
	updatedReleaseInfoIDs  []string
	createdClusterProfiles []*v1.ClusterProfile
}

func (store *currentBaselineMemoryStore) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
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
	return store.clusterProfiles, nil
}

func (store *currentBaselineMemoryStore) CreateClusterProfile(profile *v1.ClusterProfile) error {
	store.createdClusterProfiles = append(store.createdClusterProfiles, profile)
	return nil
}

type releaseInfoBuilderFunc func(string) (*v1.ReleaseInfo, error)

func (builder releaseInfoBuilderFunc) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	return builder(baseline)
}

type clusterProfileBuilderFunc func(string) ([]*v1.ClusterProfile, error)

func (builder clusterProfileBuilderFunc) BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error) {
	return builder(baseline)
}

func releaseInfoBuilderOutput(name, defaultClusterVersion string, compatibleBaselines []string) *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: name},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      defaultClusterVersion,
			CompatibleClusterBaselines: compatibleBaselines,
		},
	}
}

func mustCommunityProfile(t *testing.T, version string) *v1.ClusterProfile {
	t.Helper()
	profile, err := releaseprofile.CommunityClusterProfile(version)
	require.NoError(t, err)
	return profile
}

func completeProfile(name string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: name},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: name},
				NodeAgent:    v1.ImageRef{Image: "neutree/node-agent", Tag: name},
				NodeExporter: v1.ImageRef{Image: "prom/node-exporter", Tag: name},
				VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: name},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: name},
				Router:            v1.ImageRef{Image: "neutree/router", Tag: name},
				NodeAgent:         v1.ImageRef{Image: "neutree/node-agent", Tag: name},
				NodeExporter:      v1.ImageRef{Image: "prom/node-exporter", Tag: name},
				VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: name},
				KubeStateMetrics:  v1.ImageRef{Image: "kube-state-metrics/kube-state-metrics", Tag: name},
			},
		}},
	}
}

func assertCompleteProfile(t *testing.T, profile *v1.ClusterProfile) {
	t.Helper()
	require.NotNil(t, profile)
	require.NotNil(t, profile.Spec)
	_, found := profile.Spec.ComponentsFor(v1.SSHClusterType)
	assert.True(t, found)
	_, found = profile.Spec.ComponentsFor(v1.KubernetesClusterType)
	assert.True(t, found)
}

func createdProfileNames(profiles []*v1.ClusterProfile) []string {
	names := make([]string, 0, len(profiles))
	for _, profile := range profiles {
		names = append(names, profile.GetName())
	}

	return names
}
