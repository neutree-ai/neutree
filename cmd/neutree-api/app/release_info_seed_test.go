package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/cluster/releaseinfo"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestAppRunStopsWhenReleaseInfoSeedFails(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	application := NewApp(&config.APIConfig{
		Storage: store,
		Version: "v1.2.0",
	})

	var gotVersion string
	application.synchronizeReleaseInfo = func(_ releaseinfo.Store, version string) (releaseinfo.SyncResult, error) {
		gotVersion = version
		return releaseinfo.SyncResult{}, errors.New("database unavailable")
	}

	err := application.Run(context.Background())
	require.ErrorContains(t, err, "synchronize release info")
	require.Equal(t, "v1.2.0", gotVersion)
}

func TestAppSkipsReleaseInfoSeedForDevelopmentBuild(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	application := NewApp(&config.APIConfig{
		Storage: store,
		Version: "dev",
	})

	called := false
	application.synchronizeReleaseInfo = func(_ releaseinfo.Store, _ string) (releaseinfo.SyncResult, error) {
		called = true
		return releaseinfo.SyncResult{}, nil
	}

	require.NoError(t, application.synchronizeCurrentReleaseInfo())
	require.False(t, called)
}
