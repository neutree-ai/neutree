package operator

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/operator/manifests"
	clusterversion "github.com/neutree-ai/neutree/internal/cluster/version"
	publicaccelerator "github.com/neutree-ai/neutree/pkg/accelerator"
)

type NvidiaResourceRenderer struct {
	acceleratorMgr publicaccelerator.ProfileProvider
}

func NewNvidiaResourceRenderer(acceleratorMgr publicaccelerator.ProfileProvider) *NvidiaResourceRenderer {
	return &NvidiaResourceRenderer{acceleratorMgr: acceleratorMgr}
}

func (r *NvidiaResourceRenderer) Render(ctx context.Context, opCtx *OperatorContext, state ProviderState, plan ComponentPlan) (*unstructured.UnstructuredList, error) {
	nodeAgent, err := r.RenderNodeAgent(ctx, opCtx, state, plan)
	if err != nil {
		return nil, err
	}
	exporter, err := r.RenderExporter(ctx, opCtx, state, plan)
	if err != nil {
		return nil, err
	}
	virtualization, err := r.RenderAcceleratorVirtualization(ctx, opCtx, state, plan)
	if err != nil {
		return nil, err
	}
	resources := &unstructured.UnstructuredList{}
	resources.Items = append(resources.Items, nodeAgent.Items...)
	resources.Items = append(resources.Items, exporter.Items...)
	resources.Items = append(resources.Items, virtualization.Items...)
	for index := range resources.Items {
		if err := AddDependencySelectorToDaemonSet(&resources.Items[index], state); err != nil {
			return nil, err
		}
	}
	return resources, nil
}

func (r *NvidiaResourceRenderer) RenderNodeAgent(ctx context.Context, opCtx *OperatorContext, state ProviderState, _ ComponentPlan) (*unstructured.UnstructuredList, error) {
	resources, err := r.renderBase(ctx, opCtx, state)
	if err != nil {
		return nil, err
	}
	return selectNvidiaScopeResources(resources, func(object *unstructured.Unstructured) bool {
		if object.GetKind() == daemonSetKind || object.GetKind() == "ServiceAccount" {
			return object.GetName() == manifests.NodeAgentName
		}
		return (object.GetKind() == "ClusterRole" || object.GetKind() == "ClusterRoleBinding") && strings.HasPrefix(object.GetName(), manifests.NodeAgentName+"-")
	}), nil
}

func (r *NvidiaResourceRenderer) RenderExporter(ctx context.Context, opCtx *OperatorContext, state ProviderState, _ ComponentPlan) (*unstructured.UnstructuredList, error) {
	resources, err := r.renderBase(ctx, opCtx, state)
	if err != nil {
		return nil, err
	}
	return selectNvidiaScopeResources(resources, func(object *unstructured.Unstructured) bool {
		return (object.GetKind() == daemonSetKind && object.GetName() == manifests.ExporterName) || (object.GetKind() == "ConfigMap" && object.GetName() == manifests.ExporterName+"-config")
	}), nil
}

func (r *NvidiaResourceRenderer) RenderAcceleratorVirtualization(ctx context.Context, opCtx *OperatorContext, state ProviderState, _ ComponentPlan) (*unstructured.UnstructuredList, error) {
	if opCtx == nil || opCtx.Cluster == nil || opCtx.Client == nil {
		return nil, fmt.Errorf("operator context is required")
	}
	if r == nil || r.acceleratorMgr == nil {
		return nil, fmt.Errorf("NVIDIA accelerator manager is required")
	}
	if opCtx.Cluster.Spec == nil || !opCtx.Cluster.Spec.AcceleratorVirtualizationEnabled() {
		return &unstructured.UnstructuredList{}, nil
	}
	config, err := r.ResolveVirtualizationConfig(ctx, opCtx)
	if err != nil {
		return nil, err
	}
	if config == nil {
		return &unstructured.UnstructuredList{}, nil
	}
	if !config.Supported {
		if len(config.BlockingReasons) > 0 {
			return nil, fmt.Errorf("NVIDIA accelerator virtualization is unsupported: %s", strings.Join(config.BlockingReasons, "; "))
		}
		return nil, fmt.Errorf("NVIDIA accelerator virtualization mode is unsupported")
	}
	if len(config.BlockingReasons) > 0 {
		return nil, fmt.Errorf("accelerator plugin blocked HAMi virtualization: %s", strings.Join(config.BlockingReasons, "; "))
	}
	version, err := clusterversion.HAMiVersionForCluster(opCtx.Cluster.GetVersion())
	if err != nil {
		return nil, err
	}
	nodeSelector, driverRoot, err := manifests.NvidiaDevicePluginConfig(config.ConfigPatch)
	if err != nil {
		return nil, err
	}
	devicePlugin, err := manifests.RenderNvidiaDevicePlugin(manifests.NvidiaDevicePluginRenderOptions{Namespace: opCtx.Namespace, ImagePrefix: opCtx.ImagePrefix, ImagePullSecret: opCtx.ImagePullSecret, Version: version, NodeSelector: nodeSelector, DriverRoot: driverRoot, Managed: state.Managed})
	if err != nil {
		return nil, err
	}
	if err := addHamiConfigChecksums(ctx, opCtx, devicePlugin); err != nil {
		return nil, err
	}
	return devicePlugin, nil
}

func (r *NvidiaResourceRenderer) renderBase(ctx context.Context, opCtx *OperatorContext, state ProviderState) (*unstructured.UnstructuredList, error) {
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
	exporter, err := resolveNvidiaExporterTarget(opCtx, profile)
	if err != nil {
		return nil, err
	}
	return manifests.RenderNvidia(ctx, manifests.NvidiaRenderOptions{Cluster: opCtx.Cluster, Namespace: opCtx.Namespace, ImagePrefix: opCtx.ImagePrefix, ImagePullSecret: opCtx.ImagePullSecret, Profile: profile, Managed: state.Managed, ExporterTarget: exporter.Target, ExternalExporter: !exporter.Managed})
}

func selectNvidiaScopeResources(resources *unstructured.UnstructuredList, keep func(*unstructured.Unstructured) bool) *unstructured.UnstructuredList {
	selected := &unstructured.UnstructuredList{}
	for index := range resources.Items {
		if keep(&resources.Items[index]) {
			selected.Items = append(selected.Items, resources.Items[index])
		}
	}
	return selected
}

func (r *NvidiaResourceRenderer) ResolveVirtualizationConfig(ctx context.Context, opCtx *OperatorContext) (*publicaccelerator.VirtualizationConfig, error) {
	if r == nil || r.acceleratorMgr == nil {
		return nil, fmt.Errorf("NVIDIA accelerator manager is required")
	}
	plugin, ok := r.acceleratorMgr.GetPlugin(NVIDIAAcceleratorType)
	if !ok || plugin == nil {
		return nil, fmt.Errorf("NVIDIA accelerator plugin is not registered")
	}
	if !publicaccelerator.PluginMatchesCluster(plugin, opCtx.Cluster) {
		return nil, nil
	}
	provider, ok := plugin.(publicaccelerator.ClusterVirtualizationConfigProvider)
	if !ok {
		return nil, nil
	}
	return provider.ResolveClusterVirtualizationConfig(ctx, opCtx.Cluster)
}

// HAMi uses subPath mounts, so control-plane configuration changes require a plugin rollout.
func addHamiConfigChecksums(ctx context.Context, opCtx *OperatorContext, resources *unstructured.UnstructuredList) error {
	checksums := map[string]string{}

	for name, annotation := range map[string]string{
		"hami-scheduler": "checksum/hami-scheduler-config", "hami-scheduler-device": "checksum/hami-scheduler-device-config",
	} {
		config := &corev1.ConfigMap{}
		if err := opCtx.Client.Get(ctx, client.ObjectKey{Namespace: opCtx.Namespace, Name: name}, config); err != nil {
			if client.IgnoreNotFound(err) == nil {
				continue
			}

			return err
		}

		data, err := json.Marshal(config.Data)
		if err != nil {
			return err
		}

		checksums[annotation] = fmt.Sprintf("%x", sha256.Sum256(data))
	}

	for index := range resources.Items {
		object := &resources.Items[index]
		if object.GetKind() != daemonSetKind {
			continue
		}

		annotations := map[string]string{}

		value, _, err := unstructured.NestedFieldNoCopy(object.Object, "spec", "template", "metadata", "annotations")
		if err != nil {
			return err
		}

		if value != nil {
			annotations, _, err = unstructured.NestedStringMap(object.Object, "spec", "template", "metadata", "annotations")
			if err != nil {
				return err
			}
		}

		for key, checksum := range checksums {
			annotations[key] = checksum
		}

		if err := unstructured.SetNestedStringMap(object.Object, annotations, "spec", "template", "metadata", "annotations"); err != nil {
			return err
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

	if object.GetKind() != daemonSetKind {
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
	if object == nil || object.GetKind() != daemonSetKind || !state.Managed {
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

	if err := unstructured.SetNestedStringMap(object.Object, selector, "spec", "template", "spec", "nodeSelector"); err != nil {
		return err
	}

	if state.ContainerRuntime != containerdRuntime {
		return nil
	}

	// Sandbox runtime selection happens before toolkit-validation can wait for dependencies.
	runtimeClass := state.RuntimeClass
	if runtimeClass == "" {
		runtimeClass = DefaultNVIDIARuntimeClass
	}

	return unstructured.SetNestedField(object.Object, runtimeClass, "spec", "template", "spec", "runtimeClassName")
}

func SortResources(resources *unstructured.UnstructuredList) {
	sort.SliceStable(resources.Items, func(i, j int) bool {
		return resources.Items[i].GetKind()+"/"+resources.Items[i].GetName() < resources.Items[j].GetKind()+"/"+resources.Items[j].GetName()
	})
}
