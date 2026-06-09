package hami

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

func (h *HAMiComponent) ReconcileNodeScope(ctx context.Context) (NodeScopePlan, error) {
	nodeList := &corev1.NodeList{}
	if err := h.ctrlClient.List(ctx, nodeList); err != nil {
		return NodeScopePlan{}, errors.Wrap(err, "failed to list nodes")
	}

	plan, err := h.planNodeScope(ctx, nodeList.Items, h.cluster.Spec.AcceleratorVirtualizationEnabled())
	if err != nil {
		return plan, err
	}

	for nodeName, labels := range plan.Patches {
		node := &corev1.Node{}
		if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: nodeName}, node); err != nil {
			return plan, errors.Wrapf(err, "failed to get node %s", nodeName)
		}

		if node.Labels == nil {
			node.Labels = map[string]string{}
		}
		for key, value := range labels {
			node.Labels[key] = value
		}

		if err := h.ctrlClient.Update(ctx, node); err != nil {
			return plan, errors.Wrapf(err, "failed to patch node %s", nodeName)
		}
	}

	return plan, nil
}

func (h *HAMiComponent) planNodeScope(ctx context.Context, nodes []corev1.Node, enabled bool) (NodeScopePlan, error) {
	config, err := h.resolveVirtualizationConfig(ctx)
	if err != nil {
		return NodeScopePlan{}, err
	}

	if err := virtualizationConfigBlocked(config); err != nil {
		return NodeScopePlan{}, err
	}

	plan := PlanNodeScope(nodes, config.CandidateNodes, nodeScopeLabelFromPlugin(config.NodeScopeLabel), enabled)
	plan.ConfigPatch = config.ConfigPatch

	return plan, nil
}

func (h *HAMiComponent) resolveVirtualizationConfig(
	ctx context.Context,
) (*plugin.VirtualizationConfig, error) {
	if h.pluginProvider == nil {
		return nil, errors.New("accelerator plugin provider is not configured")
	}

	configs := make([]*plugin.VirtualizationConfig, 0)
	for _, acceleratorType := range h.pluginProvider.SupportPlugins() {
		acceleratorPlugin, ok := h.pluginProvider.GetPlugin(acceleratorType)
		if !ok || acceleratorPlugin == nil {
			continue
		}

		configProvider, ok := acceleratorPlugin.(plugin.ClusterVirtualizationConfigProvider)
		if !ok {
			configs = append(configs, unsupportedVirtualizationConfig(acceleratorType))
			continue
		}

		config, err := configProvider.ResolveClusterVirtualizationConfig(ctx, h.cluster)
		if err != nil {
			return nil, err
		}
		if config == nil {
			return nil, errors.Errorf("accelerator plugin %s returned nil virtualization config", acceleratorType)
		}
		configs = append(configs, config)
	}

	config, err := mergeVirtualizationConfigs(configs)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return nil, errors.New("accelerator plugin returned nil virtualization config")
	}

	return config, nil
}

func unsupportedVirtualizationConfig(acceleratorType string) *plugin.VirtualizationConfig {
	return plugin.NewUnsupportedVirtualizationConfig(acceleratorType)
}

func mergeVirtualizationConfigs(configs []*plugin.VirtualizationConfig) (*plugin.VirtualizationConfig, error) {
	if len(configs) == 0 {
		return nil, errors.New("no accelerator plugins are registered")
	}

	merged := &plugin.VirtualizationConfig{
		Supported:       false,
		BlockingReasons: []string{},
		CandidateNodes:  []string{},
		ConfigPatch:     map[string]interface{}{},
	}
	unsupportedReasons := make([]string, 0)

	for _, config := range configs {
		if config == nil {
			continue
		}
		if !config.Supported {
			unsupportedReasons = append(unsupportedReasons, config.BlockingReasons...)
			continue
		}

		merged.Supported = true
		merged.BlockingReasons = append(merged.BlockingReasons, config.BlockingReasons...)
		merged.CandidateNodes = appendUniqueStrings(merged.CandidateNodes, config.CandidateNodes)
		if merged.NodeScopeLabel.Key == "" && config.NodeScopeLabel.Key != "" {
			merged.NodeScopeLabel = config.NodeScopeLabel
		}
		merged.ConfigPatch = mergeConfigPatch(merged.ConfigPatch, config.ConfigPatch)
	}

	if !merged.Supported {
		merged.BlockingReasons = unsupportedReasons
	}

	return merged, nil
}

func appendUniqueStrings(target []string, values []string) []string {
	seen := make(map[string]struct{}, len(target)+len(values))
	for _, value := range target {
		seen[value] = struct{}{}
	}

	for _, value := range values {
		if _, ok := seen[value]; ok {
			continue
		}
		target = append(target, value)
		seen[value] = struct{}{}
	}

	return target
}

func mergeConfigPatch(target map[string]interface{}, patch map[string]interface{}) map[string]interface{} {
	if target == nil {
		target = map[string]interface{}{}
	}
	for key, patchValue := range patch {
		patchMap, patchIsMap := patchValue.(map[string]interface{})
		targetMap, targetIsMap := target[key].(map[string]interface{})
		if patchIsMap && targetIsMap {
			target[key] = mergeConfigPatch(targetMap, patchMap)
			continue
		}
		target[key] = patchValue
	}

	return target
}

func virtualizationConfigBlocked(config *plugin.VirtualizationConfig) error {
	if !config.Supported {
		return errors.New("no accelerator plugin supports HAMi virtualization on this cluster")
	}

	if len(config.BlockingReasons) > 0 {
		return errors.Errorf("accelerator plugin blocked HAMi virtualization: %s",
			strings.Join(config.BlockingReasons, "; "))
	}

	return nil
}

func nodeScopeLabelFromPlugin(label plugin.VirtualizationNodeScopeLabel) NodeScopeLabel {
	if label.Key == "" {
		return NvidiaNodeScopeLabel
	}
	if label.EnabledValue == "" {
		label.EnabledValue = NvidiaNodeScopeLabel.EnabledValue
	}
	if label.DisabledValue == "" {
		label.DisabledValue = NvidiaNodeScopeLabel.DisabledValue
	}

	return NodeScopeLabel{
		Key:           label.Key,
		EnabledValue:  label.EnabledValue,
		DisabledValue: label.DisabledValue,
	}
}

func allCandidateNodesExplicitlyDisabled(plan NodeScopePlan) bool {
	return len(plan.EnabledNodes) == 0 && len(plan.PatchedNodes) == 0 && len(plan.DisabledNodes) > 0
}
