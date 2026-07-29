package hami

import (
	"reflect"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func TestNodeScopePlanExposesSelectedDevicePluginTemplate(t *testing.T) {
	field, found := reflect.TypeOf(NodeScopePlan{}).FieldByName("DevicePluginTemplate")

	require.True(t, found)
	assert.Equal(t, reflect.Pointer, field.Type.Kind())
}

func TestMergeVirtualizationConfigsSelectsOnlyCandidateOwner(t *testing.T) {
	owner := &v1.VirtualizationConfig{
		Supported:       true,
		CandidateNodes:  []string{"nvidia-node"},
		BlockingReasons: []string{"owner-specific reason"},
		NodeScopeLabel: v1.VirtualizationNodeScopeLabel{
			Key: "neutree.ai/nvidia-vgpu-enabled",
		},
		ConfigPatch: map[string]interface{}{"devicePlugin": map[string]interface{}{"enabled": true}},
	}
	nonOwner := &v1.VirtualizationConfig{
		Supported:       true,
		BlockingReasons: []string{"non-owner reason"},
		NodeScopeLabel: v1.VirtualizationNodeScopeLabel{
			Key: "neutree.ai/ascend-vnpu-enabled",
		},
	}

	config, err := mergeVirtualizationConfigs([]*v1.VirtualizationConfig{nonOwner, owner})

	require.NoError(t, err)
	assert.Same(t, owner, config)
}

func TestMergeVirtualizationConfigsRejectsZeroOrMultipleCandidateOwners(t *testing.T) {
	candidateOwner := func(node string) *v1.VirtualizationConfig {
		return &v1.VirtualizationConfig{Supported: true, CandidateNodes: []string{node}}
	}

	tests := []struct {
		name            string
		configs         []*v1.VirtualizationConfig
		expectedReasons []string
	}{
		{
			name:            "zero owners",
			expectedReasons: []string{"no nodes", "unsupported"},
			configs: []*v1.VirtualizationConfig{
				{Supported: true, BlockingReasons: []string{"no nodes"}},
				{Supported: false, BlockingReasons: []string{"unsupported"}},
			},
		},
		{
			name:    "multiple owners",
			configs: []*v1.VirtualizationConfig{candidateOwner("nvidia-node"), candidateOwner("ascend-node")},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := mergeVirtualizationConfigs(tt.configs)

			require.Error(t, err)
			assert.Contains(t, err.Error(), "exactly one")
			for _, expectedReason := range tt.expectedReasons {
				assert.Contains(t, err.Error(), expectedReason)
			}
		})
	}
}

func TestPlanNodeScopePatchesOnlyUnsetCandidateNodes(t *testing.T) {
	nodes := []corev1.Node{
		newScopeNode("gpu-unset", map[string]string{}),
		newScopeNode("gpu-enabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}),
		newScopeNode("gpu-disabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "false"}),
		newScopeNode("stale-enabled", map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}),
	}

	plan := PlanNodeScope(nodes, []string{"gpu-unset", "gpu-enabled", "gpu-disabled"}, defaultNodeScopeLabel(), true)

	assert.Equal(t, []string{"gpu-unset"}, plan.PatchedNodes)
	assert.Equal(t, []string{"gpu-enabled"}, plan.EnabledNodes)
	assert.Equal(t, []string{"gpu-disabled"}, plan.DisabledNodes)
	assert.Equal(t, []string{"stale-enabled"}, plan.StaleEnabledNodes)
	assert.Equal(t, map[string]string{plugin.NvidiaGPUVirtualizationLabelKey: "true"}, plan.Patches["gpu-unset"])
}

func TestPlanNodeScopeDoesNotPatchWhenVirtualizationDisabled(t *testing.T) {
	nodes := []corev1.Node{
		newScopeNode("gpu-unset", map[string]string{}),
	}

	plan := PlanNodeScope(nodes, []string{"gpu-unset"}, defaultNodeScopeLabel(), false)

	assert.Empty(t, plan.PatchedNodes)
	assert.Empty(t, plan.Patches)
}

func newScopeNode(name string, labels map[string]string) corev1.Node {
	return corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   name,
			Labels: labels,
		},
	}
}
