package cluster

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
)

func TestApplyRuntimeProfiles(t *testing.T) {
	status := &v1.ClusterStatus{}
	require.NoError(t, applyRuntimeProfiles(status, map[string]string{"npu": "npu-ascend910b"}))
	require.Equal(t, "npu", *status.AcceleratorType)
	require.Equal(t, "npu-ascend910b", *status.AcceleratorRuntimeProfile)

	require.Error(t, applyRuntimeProfiles(status, map[string]string{"npu": "a", "other": "b"}))
}
