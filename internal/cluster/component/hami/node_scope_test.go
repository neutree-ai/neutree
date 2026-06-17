package hami

import (
	"testing"

	"github.com/stretchr/testify/assert"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

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
