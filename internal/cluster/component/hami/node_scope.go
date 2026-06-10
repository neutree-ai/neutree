package hami

import (
	corev1 "k8s.io/api/core/v1"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

const NvidiaVGPUEnabledLabelKey = plugin.NvidiaGPUVirtualizationLabelKey

var NvidiaNodeScopeLabel = NodeScopeLabel{
	Key:           NvidiaVGPUEnabledLabelKey,
	EnabledValue:  "true",
	DisabledValue: "false",
}

type NodeScopeLabel struct {
	Key           string
	EnabledValue  string
	DisabledValue string
}

type NodeScopePlan struct {
	EnabledNodes      []string
	DisabledNodes     []string
	StaleEnabledNodes []string
	PatchedNodes      []string
	Patches           map[string]map[string]string
	NodeScopeLabel    NodeScopeLabel
	ConfigPatch       map[string]interface{}
}

// PlanNodeScope decides which candidate nodes need the virtualization label.
// Existing disabled labels are respected; only unlabeled candidate nodes are
// patched to opt in.
func PlanNodeScope(nodes []corev1.Node, candidateNodes []string, label NodeScopeLabel, enabled bool) NodeScopePlan {
	plan := NodeScopePlan{
		Patches:        map[string]map[string]string{},
		NodeScopeLabel: label,
	}

	if !enabled {
		return plan
	}

	candidates := make(map[string]struct{}, len(candidateNodes))
	for _, node := range candidateNodes {
		candidates[node] = struct{}{}
	}

	for _, node := range nodes {
		value := node.Labels[label.Key]
		_, candidate := candidates[node.Name]

		if !candidate {
			// Do not clear stale labels automatically. A node that no longer
			// matches plugin discovery is reported to status for operator action.
			if value == label.EnabledValue {
				plan.StaleEnabledNodes = append(plan.StaleEnabledNodes, node.Name)
			}

			continue
		}

		switch value {
		case label.EnabledValue:
			plan.EnabledNodes = append(plan.EnabledNodes, node.Name)
		case label.DisabledValue:
			plan.DisabledNodes = append(plan.DisabledNodes, node.Name)
		default:
			plan.PatchedNodes = append(plan.PatchedNodes, node.Name)
			plan.Patches[node.Name] = map[string]string{
				label.Key: label.EnabledValue,
			}
		}
	}

	return plan
}
