package app

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestDefaultAdaptersReturnsFreshInstances(t *testing.T) {
	first := DefaultAdapters()
	second := DefaultAdapters()

	require.Len(t, first, 1)
	require.Len(t, second, 1)
	require.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), first[0].Type())
	require.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), second[0].Type())
	require.NotSame(t, first[0], second[0])
}
