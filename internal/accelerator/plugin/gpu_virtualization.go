package plugin

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
)

type VirtualizationNodeScopeLabel struct {
	Key           string
	EnabledValue  string
	DisabledValue string
}

type GPUOperatorClusterPolicy struct {
	Name string
	Spec map[string]interface{}
}

type VirtualizationConfigInput struct {
	Nodes                      []corev1.Node
	GPUOperatorClusterPolicies []GPUOperatorClusterPolicy
}

type VirtualizationConfig struct {
	Supported       bool
	BlockingReasons []string
	CandidateNodes  []string
	NodeScopeLabel  VirtualizationNodeScopeLabel
	ConfigPatch     map[string]interface{}
}

type VirtualizationConfigResolver interface {
	ResolveVirtualizationConfig(ctx context.Context, input VirtualizationConfigInput) (*VirtualizationConfig, error)
}

func NewUnsupportedVirtualizationConfig(acceleratorType string) *VirtualizationConfig {
	return &VirtualizationConfig{
		Supported: false,
		BlockingReasons: []string{
			fmt.Sprintf("accelerator %s does not support HAMi virtualization", acceleratorType),
		},
	}
}

func (p *GPUAcceleratorPlugin) ResolveVirtualizationConfig(
	_ context.Context,
	input VirtualizationConfigInput,
) (*VirtualizationConfig, error) {
	configPatch := map[string]interface{}{}
	blockingReasons := make([]string, 0)
	setNestedString(configPatch, NvidiaGPUTopologyAwarePolicy, "scheduler", "defaultSchedulerPolicy", "gpuSchedulerPolicy")

	for _, policy := range input.GPUOperatorClusterPolicies {
		if boolAtPathDefault(policy.Spec, true, "driver", "enabled") {
			setNestedString(configPatch, NvidiaGPUOperatorDriverRoot, "devicePlugin", "nvidiaDriverRoot")
		}
		if boolAtPathDefault(policy.Spec, true, "devicePlugin", "enabled") {
			blockingReasons = append(blockingReasons,
				"NVIDIA GPU Operator devicePlugin is enabled; disable it before enabling HAMi NVIDIA vGPU")
		}
	}

	return &VirtualizationConfig{
		Supported:       true,
		BlockingReasons: blockingReasons,
		CandidateNodes:  NvidiaVirtualizationCandidateNodes(input.Nodes),
		NodeScopeLabel: VirtualizationNodeScopeLabel{
			Key:           NvidiaGPUVirtualizationLabelKey,
			EnabledValue:  "true",
			DisabledValue: "false",
		},
		ConfigPatch: configPatch,
	}, nil
}

func NvidiaVirtualizationCandidateNodes(nodes []corev1.Node) []string {
	candidates := make([]string, 0)
	for _, node := range nodes {
		if nvidiaMIGStrategyEnabled(node.Labels) {
			continue
		}

		if node.Labels[NvidiaGPUDiscoveryLabelKey] == NvidiaGPUDiscoveryLabelValue ||
			hasPositiveResource(node.Status.Capacity, NvidiaGPUKubernetesResource) ||
			hasPositiveResource(node.Status.Allocatable, NvidiaGPUKubernetesResource) {
			candidates = append(candidates, node.Name)
		}
	}

	return candidates
}

func nvidiaMIGStrategyEnabled(labels map[string]string) bool {
	if labels == nil {
		return false
	}

	return nvidiaMIGStrategyIsEnabled(labels[NvidiaGPUMIGStrategyLabelKey])
}

func nvidiaMIGStrategyIsEnabled(strategy string) bool {
	normalized := strings.ToLower(strings.TrimSpace(strategy))
	return normalized != "" && normalized != NvidiaGPUMIGStrategyNone
}

func hasPositiveResource(resources corev1.ResourceList, name corev1.ResourceName) bool {
	quantity, ok := resources[name]
	return ok && !quantity.IsZero()
}

func boolAtPathDefault(values map[string]interface{}, defaultValue bool, path ...string) bool {
	value, ok := valueAtPath(values, path...)
	if !ok {
		return defaultValue
	}

	boolValue, ok := value.(bool)
	if !ok {
		return defaultValue
	}

	return boolValue
}

func stringAtPath(values map[string]interface{}, path ...string) (string, bool) {
	value, ok := valueAtPath(values, path...)
	if !ok {
		return "", false
	}

	stringValue, ok := value.(string)
	if !ok {
		return "", false
	}

	return stringValue, true
}

func setNestedString(values map[string]interface{}, value string, path ...string) {
	current := values
	for _, key := range path[:len(path)-1] {
		next, ok := current[key].(map[string]interface{})
		if !ok {
			next = map[string]interface{}{}
			current[key] = next
		}
		current = next
	}

	current[path[len(path)-1]] = value
}

func valueAtPath(values map[string]interface{}, path ...string) (interface{}, bool) {
	var current interface{} = values
	for _, key := range path {
		currentMap, ok := current.(map[string]interface{})
		if !ok {
			return nil, false
		}

		current, ok = currentMap[key]
		if !ok {
			return nil, false
		}
	}

	return current, true
}
