package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSynchronizeCurrentBaselineCreatesReleaseInfoAndProfiles(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	store := &baselineMemoryStore{}

	require.NoError(t, SynchronizeCurrentBaseline(store, "v1.2.0", builder))
	require.Len(t, store.createdReleaseInfos, 1)
	require.Empty(t, store.updatedReleaseInfos)
	require.Len(t, store.createdClusterProfiles, 3)
	assert.Equal(t, "v1.2.0", store.createdReleaseInfos[0].GetName())
	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, profileNames(store.createdClusterProfiles))
}

func TestSynchronizeCurrentBaselineUpdatesCurrentReleaseButNeverProfileDrift(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	matching := make([]v1.ClusterProfile, 0, len(profiles))
	for _, profile := range profiles {
		matching = append(matching, *cloneClusterProfile(profile))
	}

	store := &baselineMemoryStore{
		releaseInfos:    []v1.ReleaseInfo{{ID: 7, Metadata: &v1.Metadata{Name: "v1.2.0"}}},
		clusterProfiles: matching,
	}
	require.NoError(t, SynchronizeCurrentBaseline(store, "v1.2.0", builder))
	require.Len(t, store.updatedReleaseInfos, 1)
	require.Empty(t, store.createdClusterProfiles)

	drifted := cloneClusterProfile(profiles[0])
	ssh := drifted.Spec.Components[v1.SSHClusterType]
	ssh.RayRuntime.Tag = "drifted"
	drifted.Spec.Components[v1.SSHClusterType] = ssh

	driftStore := &baselineMemoryStore{clusterProfiles: []v1.ClusterProfile{*drifted}}
	err = SynchronizeCurrentBaseline(driftStore, "v1.2.0", builder)
	require.ErrorContains(t, err, "content drift")
	assert.Empty(t, driftStore.createdReleaseInfos)
	assert.Empty(t, driftStore.updatedReleaseInfos)
	assert.Empty(t, driftStore.createdClusterProfiles)
}

func TestSynchronizeCurrentBaselineRejectsInvalidDependenciesAndPersistedIdentifier(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

	require.ErrorContains(t, SynchronizeCurrentBaseline(nil, "v1.2.0", builder), "store is required")
	require.ErrorContains(t, SynchronizeCurrentBaseline(&baselineMemoryStore{}, "v1.2.0", nil), "builder is required")

	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)
	persisted := make([]v1.ClusterProfile, 0, len(profiles))
	for _, profile := range profiles {
		persisted = append(persisted, *cloneClusterProfile(profile))
	}

	store := &baselineMemoryStore{
		releaseInfos:    []v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "v1.2.0"}}},
		clusterProfiles: persisted,
	}
	require.ErrorContains(t, SynchronizeCurrentBaseline(store, "v1.2.0", builder), "has no identifier")
}

type baselineMemoryStore struct {
	releaseInfos    []v1.ReleaseInfo
	clusterProfiles []v1.ClusterProfile

	createdReleaseInfos    []*v1.ReleaseInfo
	updatedReleaseInfos    []*v1.ReleaseInfo
	createdClusterProfiles []*v1.ClusterProfile
}

func (store *baselineMemoryStore) ListReleaseInfo() ([]v1.ReleaseInfo, error) {
	return store.releaseInfos, nil
}

func (store *baselineMemoryStore) CreateReleaseInfo(info *v1.ReleaseInfo) error {
	store.createdReleaseInfos = append(store.createdReleaseInfos, info)
	return nil
}

func (store *baselineMemoryStore) UpdateReleaseInfo(_ string, info *v1.ReleaseInfo) error {
	store.updatedReleaseInfos = append(store.updatedReleaseInfos, info)
	return nil
}

func (store *baselineMemoryStore) ListClusterProfile() ([]v1.ClusterProfile, error) {
	return store.clusterProfiles, nil
}

func (store *baselineMemoryStore) CreateClusterProfile(profile *v1.ClusterProfile) error {
	store.createdClusterProfiles = append(store.createdClusterProfiles, profile)
	return nil
}
