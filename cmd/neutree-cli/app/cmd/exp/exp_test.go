package exp

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestNewExpCmdRegistersBench(t *testing.T) {
	cmd := NewExpCmd()
	require.Equal(t, "exp", cmd.Use)

	bench, _, err := cmd.Find([]string{"bench"})
	require.NoError(t, err)
	require.Equal(t, "bench", bench.Name())
}
