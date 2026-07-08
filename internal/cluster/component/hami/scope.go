package hami

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
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

func (h *HAMiComponent) DisableNodeScope(ctx context.Context) error {
	config, err := h.resolveVirtualizationConfig(ctx)
	if err != nil {
		return err
	}

	nodeList := &corev1.NodeList{}
	if err := h.ctrlClient.List(ctx, nodeList); err != nil {
		return errors.Wrap(err, "failed to list nodes")
	}

	// Remove only the enabled scope label written while HAMi is active.
	// Existing disabled labels are preserved because users can set them
	// explicitly to keep a node out of virtualization. The Neutree device
	// annotation is HAMi/vGPU state and is removed from every node while the
	// cluster still owns HAMi scope, including retries after labels are gone.
	label := nodeScopeLabelFromPlugin(config.NodeScopeLabel)
	for _, item := range nodeList.Items {
		if !nodeNeedsScopeCleanup(item, label) {
			continue
		}

		node := &corev1.Node{}
		if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: item.Name}, node); err != nil {
			return errors.Wrapf(err, "failed to get node %s", item.Name)
		}

		if !cleanupNodeScope(node, label) {
			continue
		}

		if err := h.ctrlClient.Update(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to patch node %s", item.Name)
		}
	}

	return nil
}

func nodeNeedsScopeCleanup(node corev1.Node, label NodeScopeLabel) bool {
	if node.Labels[label.Key] == label.EnabledValue {
		return true
	}

	_, ok := node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]

	return ok
}

func cleanupNodeScope(node *corev1.Node, label NodeScopeLabel) bool {
	changed := cleanupEnabledNodeScopeLabel(node, label)
	if cleanupDeviceAnnotation(node) {
		changed = true
	}

	return changed
}

func cleanupEnabledNodeScopeLabel(node *corev1.Node, label NodeScopeLabel) bool {
	if node.Labels == nil || node.Labels[label.Key] != label.EnabledValue {
		return false
	}

	delete(node.Labels, label.Key)

	return true
}

func cleanupDeviceAnnotation(node *corev1.Node) bool {
	if node.Annotations == nil {
		return false
	}

	if _, ok := node.Annotations[resourceparser.NeutreeAcceleratorDevicesAnnotation]; !ok {
		return false
	}

	delete(node.Annotations, resourceparser.NeutreeAcceleratorDevicesAnnotation)

	return true
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
			configs = append(configs, plugin.NewUnsupportedVirtualizationConfig(acceleratorType))
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

	// HAMi is the current virtualization solution, but each accelerator plugin
	// owns whether that accelerator can use it and which nodes/config it needs.
	config, err := mergeVirtualizationConfigs(configs)
	if err != nil {
		return nil, err
	}

	if config == nil {
		return nil, errors.New("accelerator plugin returned nil virtualization config")
	}

	return config, nil
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

		// Multiple accelerator types can contribute to the same HAMi release.
		// Unsupported plugins are ignored once at least one supported plugin
		// provides a concrete node scope and chart patch.
		merged.Supported = true
		merged.BlockingReasons = append(merged.BlockingReasons, config.BlockingReasons...)
		merged.CandidateNodes = appendUniqueStrings(merged.CandidateNodes, config.CandidateNodes)

		if config.NodeScopeLabel.Key != "" {
			if merged.NodeScopeLabel.Key != "" && merged.NodeScopeLabel.Key != config.NodeScopeLabel.Key {
				return nil, errors.Errorf(
					"accelerator plugins returned different virtualization node scope labels: %s and %s",
					merged.NodeScopeLabel.Key,
					config.NodeScopeLabel.Key,
				)
			}

			merged.NodeScopeLabel = config.NodeScopeLabel
		}

		merged.ConfigPatch = mergeChartValues(merged.ConfigPatch, config.ConfigPatch)
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
	defaultLabel := defaultNodeScopeLabel()

	if label.Key == "" {
		return defaultLabel
	}

	if label.EnabledValue == "" {
		label.EnabledValue = defaultLabel.EnabledValue
	}

	if label.DisabledValue == "" {
		label.DisabledValue = defaultLabel.DisabledValue
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
