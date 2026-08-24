package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSynchronizeCurrentBaselineCreatesExactCatalogAndDefault(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	store := &baselineMemoryStore{}

	err = SynchronizeCurrentBaseline(store, "v1.2.0", builder)
	require.NoError(t, err)
	require.Len(t, store.createdReleaseInfos, 1)
	require.Empty(t, store.updatedReleaseInfos)
	require.Len(t, store.createdClusterProfiles, 3)

	assert.Equal(t, "v1.2.0", store.createdReleaseInfos[0].GetName())
	assert.Equal(t, "v1.2.0", store.createdReleaseInfos[0].Spec.DefaultClusterVersion)
	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, profileNames(store.createdClusterProfiles))
}

func TestSynchronizeCurrentBaselineRejectsProfileDriftBeforeWriting(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	drifted := cloneClusterProfile(profileByName(t, profiles, "v1.2.0"))
	ssh, found := drifted.Spec.ComponentsFor(v1.SSHClusterType)
	require.True(t, found)
	ssh.RayRuntime.Tag = "drifted"
	drifted.Spec.Components[v1.SSHClusterType] = ssh

	store := &baselineMemoryStore{clusterProfiles: []v1.ClusterProfile{*drifted}}
	err = SynchronizeCurrentBaseline(store, "v1.2.0", builder)
	require.ErrorContains(t, err, "cluster profile v1.2.0 content drift")
	assert.Empty(t, store.createdReleaseInfos)
	assert.Empty(t, store.updatedReleaseInfos)
	assert.Empty(t, store.createdClusterProfiles)
}

func TestSynchronizeCurrentBaselineTreatsEmptyMetadataMapsAsIdentical(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	persisted := make([]v1.ClusterProfile, 0, len(profiles))
	for _, profile := range profiles {
		copy := cloneClusterProfile(profile)
		copy.Metadata.Labels = map[string]string{}
		copy.Metadata.Annotations = map[string]string{}
		persisted = append(persisted, *copy)
	}

	store := &baselineMemoryStore{clusterProfiles: persisted}
	err = SynchronizeCurrentBaseline(store, "v1.2.0", builder)
	require.NoError(t, err)
	assert.Empty(t, store.createdClusterProfiles)
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
