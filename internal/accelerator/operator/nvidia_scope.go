package operator

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

var nvidiaHAMiNodeAnnotations = []string{
	"hami.io/node-nvidia-register",
	"hami.io/node-nvidia-score",
	"hami.io/node-handshake",
	"hami.io/mutex.lock",
}

func reconcileNvidiaNodeScope(ctx context.Context, opCtx *OperatorContext, config *publicaccelerator.VirtualizationConfig) error {
	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes); err != nil {
		return err
	}

	candidates := make(map[string]struct{}, len(config.CandidateNodes))
	for _, name := range config.CandidateNodes {
		candidates[name] = struct{}{}
	}

	for _, item := range nodes.Items {
		if _, candidate := candidates[item.Name]; !candidate || item.Labels[config.NodeScopeLabel.Key] == config.NodeScopeLabel.DisabledValue || item.Labels[config.NodeScopeLabel.Key] == config.NodeScopeLabel.EnabledValue {
			continue
		}

		name := item.Name
		node := &corev1.Node{}
		if err := opCtx.Client.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
			return err
		}

		updated := node.DeepCopy()
		if updated.Labels == nil {
			updated.Labels = map[string]string{}
		}
		updated.Labels[config.NodeScopeLabel.Key] = config.NodeScopeLabel.EnabledValue
		if err := opCtx.Client.Patch(ctx, updated, client.MergeFrom(node)); err != nil {
			return err
		}
	}

	return nil
}

func cleanupNvidiaNodeScope(ctx context.Context, opCtx *OperatorContext) error {
	nodes := &corev1.NodeList{}
	if err := opCtx.Client.List(ctx, nodes); err != nil {
		return err
	}

	for index := range nodes.Items {
		node := &nodes.Items[index]
		if node.Labels[NVIDIAGPUVirtualizationLabelKey] != NVIDIAGPUVirtualizationLabelValue && !hasNvidiaHAMiAnnotation(node.Annotations) {
			continue
		}

		updated := node.DeepCopy()
		delete(updated.Labels, NVIDIAGPUVirtualizationLabelKey)
		for _, key := range nvidiaHAMiNodeAnnotations {
			delete(updated.Annotations, key)
		}
		if err := opCtx.Client.Patch(ctx, updated, client.MergeFrom(node)); err != nil {
			return err
		}
	}

	return nil
}

func hasNvidiaHAMiAnnotation(annotations map[string]string) bool {
	for _, key := range nvidiaHAMiNodeAnnotations {
		if _, found := annotations[key]; found {
			return true
		}
	}

	return false
}
