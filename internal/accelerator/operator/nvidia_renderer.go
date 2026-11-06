package operator

import (
	"context"
	"fmt"
	"sort"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

type NvidiaResourceRenderer struct {
	acceleratorMgr publicaccelerator.ProfileProvider
}

func NewNvidiaResourceRenderer(acceleratorMgr publicaccelerator.ProfileProvider) *NvidiaResourceRenderer {
	return &NvidiaResourceRenderer{acceleratorMgr: acceleratorMgr}
}

func (r *NvidiaResourceRenderer) ReconcileControlPlane(ctx context.Context, opCtx *OperatorContext) error {
	if opCtx == nil || opCtx.Cluster == nil || opCtx.Client == nil ||
		opCtx.Cluster.Spec == nil || !opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		return nil
	}

	_, err := r.resolveVirtualizationConfig(ctx, opCtx)

	return err
}

func (r *NvidiaResourceRenderer) Render(
	ctx context.Context,
	opCtx *OperatorContext,
	state ProviderState,
	plan ComponentPlan,
) (*unstructured.UnstructuredList, error) {
	if opCtx == nil || opCtx.Cluster == nil || opCtx.Client == nil {
		return nil, fmt.Errorf("operator context is required")
	}

	if r == nil || r.acceleratorMgr == nil {
		return nil, fmt.Errorf("NVIDIA accelerator manager is required")
	}

	profile, err := r.acceleratorMgr.GetAcceleratorProfile(ctx, NVIDIAAcceleratorType)
	if err != nil {
		return nil, fmt.Errorf("get NVIDIA accelerator profile: %w", err)
	}

	resources, err := manifests.RenderNvidia(ctx, manifests.NvidiaRenderOptions{
		Cluster:         opCtx.Cluster,
		Namespace:       opCtx.Namespace,
		ImagePrefix:     opCtx.ImagePrefix,
		ImagePullSecret: opCtx.ImagePullSecret,
		Profile:         profile,
		Managed:         state.Managed,
		Affinity:        NvidiaNodeAgentAffinity(),
	})
	if err != nil {
		return nil, err
	}

	if opCtx.Cluster.Spec != nil && opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		config, err := r.resolveVirtualizationConfig(ctx, opCtx)
		if err != nil {
			return nil, err
		}

		if config != nil && config.Supported {
			controlPlane, err := manifests.RenderHami(opCtx.Namespace, opCtx.ImagePrefix, config.ConfigPatch, false)
			if err != nil {
				return nil, err
			}

			resources.Items = append(resources.Items, controlPlane.Items...)

			devicePlugin, err := manifests.RenderHamiDevicePlugin(opCtx.Namespace, opCtx.ImagePrefix, config.ConfigPatch, state.Managed)
			if err != nil {
				return nil, err
			}

			resources.Items = append(resources.Items, devicePlugin.Items...)
		}
	}

	for index := range resources.Items {
		object := &resources.Items[index]
		OperatorResourceLabels(object, opCtx.Cluster)

		if object.GetKind() != daemonSetKind {
			continue
		}

		if object.GetLabels()[manifests.MetricsTargetLabel] == "node-agent" && state.Managed {
			affinity, err := runtime.DefaultUnstructuredConverter.ToUnstructured(NvidiaNodeAgentAffinity())
			if err != nil {
				return nil, err
			}

			if err := unstructured.SetNestedField(object.Object, affinity, "spec", "template", "spec", "affinity"); err != nil {
				return nil, err
			}
		}

		if err := AddDependencySelectorToDaemonSet(object, state); err != nil {
			return nil, err
		}
	}

	return resources, nil
}

func (r *NvidiaResourceRenderer) resolveVirtualizationConfig(ctx context.Context, opCtx *OperatorContext) (*publicaccelerator.VirtualizationConfig, error) {
	if r == nil || r.acceleratorMgr == nil {
		return nil, fmt.Errorf("NVIDIA accelerator manager is required")
	}

	for _, acceleratorType := range r.acceleratorMgr.SupportPlugins() {
		plugin, ok := r.acceleratorMgr.GetPlugin(acceleratorType)
		if !ok || plugin == nil {
			continue
		}

		provider, ok := plugin.(publicaccelerator.ClusterVirtualizationConfigProvider)
		if !ok {
			continue
		}

		config, err := provider.ResolveClusterVirtualizationConfig(ctx, opCtx.Cluster)
		if err != nil {
			return nil, err
		}

		if config == nil || !config.Supported {
			continue
		}

		if err := r.reconcileVirtualizationScope(ctx, opCtx.Client, config); err != nil {
			return nil, err
		}

		return config, nil
	}

	return nil, nil
}

func (r *NvidiaResourceRenderer) reconcileVirtualizationScope(ctx context.Context, ctrlClient client.Client, config *publicaccelerator.VirtualizationConfig) error {
	if config == nil || config.NodeScopeLabel.Key == "" {
		return nil
	}

	for _, name := range config.CandidateNodes {
		node := &corev1.Node{}
		if err := ctrlClient.Get(ctx, client.ObjectKey{Name: name}, node); err != nil {
			return fmt.Errorf("get virtualization candidate node %s: %w", name, err)
		}

		if node.Labels[config.NodeScopeLabel.Key] == config.NodeScopeLabel.DisabledValue {
			continue
		}

		if node.Labels[config.NodeScopeLabel.Key] == config.NodeScopeLabel.EnabledValue {
			continue
		}

		base := node.DeepCopy()

		if node.Labels == nil {
			node.Labels = map[string]string{}
		}

		node.Labels[config.NodeScopeLabel.Key] = config.NodeScopeLabel.EnabledValue
		if err := ctrlClient.Patch(ctx, node, client.MergeFrom(base)); err != nil {
			return fmt.Errorf("patch virtualization scope on node %s: %w", name, err)
		}
	}

	return nil
}

func OperatorResourceLabels(object *unstructured.Unstructured, cluster *v1.Cluster) {
	labels := object.GetLabels()
	if labels == nil {
		labels = map[string]string{}
	}

	labels[OperatorOwnerLabel] = OperatorOwnerValue
	labels[DeploymentModeLabel] = DeploymentModeValue

	if cluster != nil && cluster.Metadata != nil {
		labels[v1.NeutreeClusterLabelKey] = cluster.Metadata.Name
		labels[v1.NeutreeClusterWorkspaceLabelKey] = cluster.Metadata.Workspace
	}

	object.SetLabels(labels)

	if object.GetKind() != "DaemonSet" {
		return
	}

	templateLabels, _, err := unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "labels")
	if err != nil {
		return
	}

	if templateLabels == nil {
		templateLabels = map[string]string{}
	}

	for key, value := range labels {
		templateLabels[key] = value
	}

	_ = unstructured.SetNestedStringMap(object.Object, templateLabels, "spec", "template", "metadata", "labels")
}

func AddDependencySelectorToDaemonSet(object *unstructured.Unstructured, state ProviderState) error {
	if object == nil || object.GetKind() != "DaemonSet" || !state.Managed {
		return nil
	}

	selector, found, err := unstructured.NestedStringMap(object.Object, "spec", "template", "spec", "nodeSelector")
	if err != nil {
		return err
	}

	if !found {
		selector = map[string]string{}
	}

	selector[ClientDeployLabel] = NVIDIAGPUNodeValue

	return unstructured.SetNestedStringMap(object.Object, selector, "spec", "template", "spec", "nodeSelector")
}

func NvidiaNodeAgentAffinity() *corev1.Affinity {
	return &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
		RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{NodeSelectorTerms: []corev1.NodeSelectorTerm{
			{MatchExpressions: []corev1.NodeSelectorRequirement{{Key: NVIDIAGPUNodeLabel, Operator: corev1.NodeSelectorOpNotIn, Values: []string{"true"}}}},
			{MatchExpressions: []corev1.NodeSelectorRequirement{
				{Key: NVIDIAGPUNodeLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"true"}},
				{Key: ClientDeployLabel, Operator: corev1.NodeSelectorOpIn, Values: []string{"true"}},
			}},
		}},
	}}
}

func SortResources(resources *unstructured.UnstructuredList) {
	sort.SliceStable(resources.Items, func(i, j int) bool {
		return resources.Items[i].GetKind()+"/"+resources.Items[i].GetName() < resources.Items[j].GetKind()+"/"+resources.Items[j].GetName()
	})
}
