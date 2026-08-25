package nodeagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type emptyTypeAdapter struct{}

func (emptyTypeAdapter) Type() string {
	return ""
}

func (emptyTypeAdapter) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

func TestRunRejectsInvalidAdapterBeforeStarting(t *testing.T) {
	err := Run(context.Background(), Config{
		Adapters: []adapter.Accelerator{emptyTypeAdapter{}},
	})

	require.ErrorContains(t, err, "accelerator adapter type is required")
}

func TestDefaultAdaptersReturnsIndependentSlice(t *testing.T) {
	first := DefaultAdapters()
	first = append(first, emptyTypeAdapter{})

	require.Empty(t, DefaultAdapters())
}
