package releaseprofile

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestSynchronizeCurrentBaselineCreatesReleaseInfoAndProfiles(t *testing.T) {
	store := &baselineMemoryStore{}

	require.NoError(t, SynchronizeCurrentBaseline(store, store))
	require.Len(t, store.createdReleaseInfos, 1)
	require.Empty(t, store.updatedReleaseInfos)
	require.Len(t, store.createdClusterProfiles, 3)
	assert.Equal(t, "v1.2.0", store.createdReleaseInfos[0].GetName())
	assert.ElementsMatch(t, []string{"v1.1.0", "v1.1.1", "v1.2.0"}, profileNames(store.createdClusterProfiles))
}

func TestSynchronizeCurrentBaselineOverwritesExistingReleaseInfoAndProfiles(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)
	profiles, err := builder.BuildClusterProfiles("v1.2.0")
	require.NoError(t, err)

	existingProfiles := make([]v1.ClusterProfile, 0, len(profiles))
	for index, profile := range profiles {
		existing := cloneClusterProfile(profile)
		existing.ID = index + 1
		existingProfiles = append(existingProfiles, *existing)
	}
	ssh := existingProfiles[0].Spec.Components[v1.SSHClusterType]
	ssh.RayRuntime.Tag = "drifted"
	existingProfiles[0].Spec.Components[v1.SSHClusterType] = ssh

	store := &baselineMemoryStore{
		releaseInfos:    []v1.ReleaseInfo{{ID: 7, Metadata: &v1.Metadata{Name: "v1.2.0"}}},
		clusterProfiles: existingProfiles,
	}
	require.NoError(t, SynchronizeCurrentBaseline(store, store))
	require.Len(t, store.updatedReleaseInfos, 1)
	require.Len(t, store.updatedClusterProfiles, 3)
	require.Empty(t, store.createdClusterProfiles)
	assert.Equal(t, profiles[0].Spec.Components[v1.SSHClusterType].RayRuntime.Tag, store.updatedClusterProfiles[0].Spec.Components[v1.SSHClusterType].RayRuntime.Tag)
}

func TestSynchronizeCurrentBaselineRejectsPersistedRecordWithoutIdentifier(t *testing.T) {
	builder, err := NewBuilderForCatalog(BuiltinCatalog())
	require.NoError(t, err)

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
	require.ErrorContains(t, SynchronizeCurrentBaseline(store, store), "has no identifier")
}

type baselineMemoryStore struct {
	releaseInfos    []v1.ReleaseInfo
	clusterProfiles []v1.ClusterProfile

	createdReleaseInfos    []*v1.ReleaseInfo
	updatedReleaseInfos    []*v1.ReleaseInfo
	createdClusterProfiles []*v1.ClusterProfile
	updatedClusterProfiles []*v1.ClusterProfile
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

func (store *baselineMemoryStore) ListClusterProfile(storage.ListOption) ([]v1.ClusterProfile, error) {
	return store.clusterProfiles, nil
}

func (store *baselineMemoryStore) CreateClusterProfile(profile *v1.ClusterProfile) error {
	store.createdClusterProfiles = append(store.createdClusterProfiles, profile)
	return nil
}

func (store *baselineMemoryStore) UpdateClusterProfile(_ string, profile *v1.ClusterProfile) error {
	store.updatedClusterProfiles = append(store.updatedClusterProfiles, profile)
	return nil
}
