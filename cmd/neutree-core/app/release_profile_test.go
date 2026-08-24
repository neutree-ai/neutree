package app

import (
	"context"
	"errors"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/controllers"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestAppRunStopsWhenCurrentReleaseSynchronizationFails(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	syncErr := errors.New("database unavailable")
	store.On("ListReleaseInfo").Return(nil, syncErr).Once()
	application := NewApp(&config.CoreConfig{Storage: store}, map[string]controllers.Controller{})

	err := application.Run(context.Background())
	require.ErrorIs(t, err, syncErr)
	require.ErrorContains(t, err, "synchronize current release info")
	store.AssertExpectations(t)
}
