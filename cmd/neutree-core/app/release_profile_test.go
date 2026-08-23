package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/pkg/releaseprofile"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestNewAppUsesReleaseProfileBuilder(t *testing.T) {
	application := NewApp(&config.CoreConfig{}, map[string]controllers.Controller{})
	assert.NotNil(t, application.releaseProfileBuilder)
}

func TestAppRunStopsWhenCurrentReleaseSynchronizationFails(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{}, nil).Once()
	application := NewApp(&config.CoreConfig{Storage: store, Version: "v1.2.0"}, map[string]controllers.Controller{})
	syncErr := errors.New("database unavailable")

	var gotBaseline string
	application.synchronizeCurrentBaseline = func(
		_ releaseprofile.CurrentBaselineStore,
		baseline string,
		_ releaseprofile.Builder,
	) error {
		gotBaseline = baseline
		return syncErr
	}

	err := application.Run(context.Background())
	require.ErrorIs(t, err, syncErr)
	assert.Equal(t, "v1.2.0", gotBaseline)
	store.AssertExpectations(t)
}

func TestCurrentControlPlaneBaseline(t *testing.T) {
	tests := []struct {
		name            string
		identity        string
		infos           []v1.ReleaseInfo
		builder         releaseprofile.Builder
		wantBaseline    string
		wantSynchronize bool
	}{
		{
			name:     "development uses highest persisted baseline",
			identity: "dev",
			infos: []v1.ReleaseInfo{
				{Metadata: &v1.Metadata{Name: "v1.1.0"}},
				{Metadata: &v1.Metadata{Name: "v1.2.0"}},
			},
			wantBaseline:    "v1.2.0",
			wantSynchronize: false,
		},
		{
			name:            "development bootstraps catalog only without persisted data",
			identity:        "dev",
			builder:         staticReleaseProfileBuilder{baseline: "v1.2.0"},
			wantBaseline:    "v1.2.0",
			wantSynchronize: true,
		},
		{
			name:            "release candidate keeps exact identity",
			identity:        "v1.2.0-rc.1",
			wantBaseline:    "v1.2.0-rc.1",
			wantSynchronize: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			store.On("ListReleaseInfo").Return(tt.infos, nil).Once()
			application := NewApp(&config.CoreConfig{Storage: store, Version: tt.identity}, map[string]controllers.Controller{})
			if tt.builder != nil {
				application.releaseProfileBuilder = tt.builder
			}

			resolution, err := application.currentControlPlaneBaseline()
			require.NoError(t, err)
			assert.Equal(t, tt.wantBaseline, resolution.name)
			assert.Equal(t, tt.wantSynchronize, resolution.shouldSynchronize)
			store.AssertExpectations(t)
		})
	}
}

func TestCurrentBaselineStoreDelegatesToStorage(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	info := releaseInfoForCoreTest()
	profile := clusterProfileForCoreTest()
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{*info}, nil).Once()
	store.On("CreateReleaseInfo", info).Return(nil).Once()
	store.On("UpdateReleaseInfo", "7", info).Return(nil).Once()
	store.On("ListClusterProfile", storage.ListOption{}).Return([]v1.ClusterProfile{*profile}, nil).Once()
	store.On("CreateClusterProfile", profile).Return(nil).Once()

	adapter := currentBaselineStore{storage: store}
	infos, err := adapter.ListReleaseInfo()
	require.NoError(t, err)
	assert.Equal(t, []v1.ReleaseInfo{*info}, infos)
	require.NoError(t, adapter.CreateReleaseInfo(info))
	require.NoError(t, adapter.UpdateReleaseInfo("7", info))
	profiles, err := adapter.ListClusterProfile()
	require.NoError(t, err)
	assert.Equal(t, []v1.ClusterProfile{*profile}, profiles)
	require.NoError(t, adapter.CreateClusterProfile(profile))
	store.AssertExpectations(t)
}

func TestCurrentControlPlaneBaselineReportsInvalidIdentity(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{}, nil).Once()
	application := NewApp(&config.CoreConfig{Storage: store, Version: "not-a-version"}, map[string]controllers.Controller{})

	_, err := application.currentControlPlaneBaseline()
	require.ErrorContains(t, err, "resolve current control-plane baseline")
	store.AssertExpectations(t)
}

func TestCurrentControlPlaneBaselineDoesNotBootstrapOverInvalidPersistedData(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{{Metadata: &v1.Metadata{Name: "invalid"}}}, nil).Once()
	application := NewApp(&config.CoreConfig{Storage: store, Version: "dev"}, map[string]controllers.Controller{})
	application.releaseProfileBuilder = staticReleaseProfileBuilder{baseline: "v1.2.0"}

	_, err := application.currentControlPlaneBaseline()
	require.ErrorContains(t, err, "resolve current control-plane baseline")
	store.AssertExpectations(t)
}

type staticReleaseProfileBuilder struct {
	baseline string
}

func (builder staticReleaseProfileBuilder) CurrentReleaseInfoBaseline() string {
	return builder.baseline
}

func (staticReleaseProfileBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

func (staticReleaseProfileBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func releaseInfoForCoreTest() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{Metadata: &v1.Metadata{Name: "v1.2.0"}}
}

func clusterProfileForCoreTest() *v1.ClusterProfile {
	return &v1.ClusterProfile{Metadata: &v1.Metadata{Name: "v1.2.0"}}
}
