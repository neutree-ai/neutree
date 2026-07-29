package hami

import (
	"context"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
)

const (
	hamiNodeNVIDIARegisterAnnotation  = "hami.io/node-nvidia-register"
	hamiNodeNVIDIAScoreAnnotation     = "hami.io/node-nvidia-score"
	hamiNodeNVIDIAHandshakeAnnotation = "hami.io/node-handshake"
	hamiNodeLockAnnotation            = "hami.io/mutex.lock"
)

var hamiNodeAnnotations = []string{
	hamiNodeNVIDIARegisterAnnotation,
	hamiNodeNVIDIAScoreAnnotation,
	hamiNodeNVIDIAHandshakeAnnotation,
	hamiNodeLockAnnotation,
}

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
	configs, err := h.resolveVirtualizationConfigs(ctx)
	if err != nil {
		return err
	}

	nodeList := &corev1.NodeList{}
	if err := h.ctrlClient.List(ctx, nodeList); err != nil {
		return errors.Wrap(err, "failed to list nodes")
	}

	// Remove only HAMi-owned node scope state. Existing disabled labels are
	// preserved because users can set them explicitly to keep a node out of
	// virtualization.
	labels := supportedNodeScopeLabels(configs)
	for _, item := range nodeList.Items {
		if !nodeNeedsScopeCleanup(item, labels) {
			continue
		}

		node := &corev1.Node{}
		if err := h.ctrlClient.Get(ctx, types.NamespacedName{Name: item.Name}, node); err != nil {
			return errors.Wrapf(err, "failed to get node %s", item.Name)
		}

		if !cleanupNodeScope(node, labels) {
			continue
		}

		if err := h.ctrlClient.Update(ctx, node); err != nil {
			return errors.Wrapf(err, "failed to patch node %s", item.Name)
		}
	}

	return nil
}

func nodeNeedsScopeCleanup(node corev1.Node, labels []NodeScopeLabel) bool {
	for _, label := range labels {
		if node.Labels[label.Key] == label.EnabledValue {
			return true
		}
	}

	return hasHAMiNodeAnnotation(node.Annotations)
}

func cleanupNodeScope(node *corev1.Node, labels []NodeScopeLabel) bool {
	changed := false

	for _, label := range labels {
		if cleanupEnabledNodeScopeLabel(node, label) {
			changed = true
		}
	}

	if cleanupHAMiNodeAnnotations(node) {
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

func cleanupHAMiNodeAnnotations(node *corev1.Node) bool {
	if node.Annotations == nil {
		return false
	}

	changed := false

	for _, key := range hamiNodeAnnotations {
		if _, ok := node.Annotations[key]; ok {
			delete(node.Annotations, key)

			changed = true
		}
	}

	return changed
}

func hasHAMiNodeAnnotation(annotations map[string]string) bool {
	for _, key := range hamiNodeAnnotations {
		if _, ok := annotations[key]; ok {
			return true
		}
	}

	return false
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
	plan.DevicePluginTemplate = config.DevicePluginTemplate

	return plan, nil
}

func (h *HAMiComponent) resolveVirtualizationConfig(
	ctx context.Context,
) (*plugin.VirtualizationConfig, error) {
	configs, err := h.resolveVirtualizationConfigs(ctx)
	if err != nil {
		return nil, err
	}

	return mergeVirtualizationConfigs(configs)
}

func (h *HAMiComponent) resolveVirtualizationConfigs(
	ctx context.Context,
) ([]*plugin.VirtualizationConfig, error) {
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

	if len(configs) == 0 {
		return nil, errors.New("no accelerator plugins are registered")
	}

	return configs, nil
}

func mergeVirtualizationConfigs(configs []*plugin.VirtualizationConfig) (*plugin.VirtualizationConfig, error) {
	owners := make([]*plugin.VirtualizationConfig, 0, len(configs))

	for _, config := range configs {
		if config != nil && config.Supported && len(config.CandidateNodes) > 0 {
			owners = append(owners, config)
		}
	}

	if len(owners) == 0 {
		blockingReasons := make([]string, 0)

		for _, config := range configs {
			if config != nil {
				blockingReasons = append(blockingReasons, config.BlockingReasons...)
			}
		}

		if len(blockingReasons) > 0 {
			return nil, errors.Errorf("exactly one accelerator plugin must own HAMi virtualization, found 0: %s",
				strings.Join(blockingReasons, "; "))
		}
	}

	if len(owners) != 1 {
		return nil, errors.Errorf("exactly one accelerator plugin must own HAMi virtualization, found %d", len(owners))
	}

	return owners[0], nil
}

func supportedNodeScopeLabels(configs []*plugin.VirtualizationConfig) []NodeScopeLabel {
	labels := make([]NodeScopeLabel, 0, len(configs))
	seen := make(map[string]struct{}, len(configs))

	for _, config := range configs {
		if config == nil || !config.Supported || config.NodeScopeLabel.Key == "" {
			continue
		}

		label := nodeScopeLabelFromPlugin(config.NodeScopeLabel)
		if _, found := seen[label.Key]; found {
			continue
		}

		labels = append(labels, label)
		seen[label.Key] = struct{}{}
	}

	return labels
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
