package plugin

import (
	"context"
	"fmt"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/accelerator"
)

// nvidiaSupportedVirtualizationModes is the single source of truth for the
// modes this accelerator supports: the config declaration and the
// cluster-level mode rejection both read from it. NVIDIA has no template
// (hard-slice) form — gpumem/gpucores are always free parameters.
var nvidiaSupportedVirtualizationModes = []v1.AcceleratorVirtualizationMode{
	v1.AcceleratorVirtualizationModeCore,
}

func (p *GPUAcceleratorPlugin) ResolveClusterVirtualizationConfig(
	ctx context.Context,
	cluster *v1.Cluster,
) (*accelerator.VirtualizationConfig, error) {
	if cluster.Spec != nil &&
		cluster.Spec.AcceleratorVirtualization != nil &&
		cluster.Spec.AcceleratorVirtualization.Mode != "" {
		if _, ok := accelerator.ResolveEffectiveMode(
			cluster.Spec.AcceleratorVirtualization.Mode,
			v1.AcceleratorVirtualizationModeCore,
			nvidiaSupportedVirtualizationModes,
		); !ok {
			return &accelerator.VirtualizationConfig{
				Supported: false,
				BlockingReasons: []string{
					fmt.Sprintf("mode %s is not supported by nvidia_gpu accelerator virtualization (supported modes: %s)",
						cluster.Spec.AcceleratorVirtualization.Mode, v1.AcceleratorVirtualizationModeCore),
				},
			}, nil
		}
	}

	ctrlClient, err := kubernetesClientForVirtualizationConfig(cluster)
	if err != nil {
		return nil, err
	}

	nodes, err := listNvidiaVirtualizationNodes(ctx, ctrlClient)
	if err != nil {
		return nil, err
	}

	clusterPolicies, err := accelerator.ListGPUOperatorClusterPolicies(ctx, ctrlClient)
	if err != nil {
		return nil, err
	}

	return p.ResolveVirtualizationConfig(ctx, VirtualizationConfigInput{
		Nodes:                      nodes,
		GPUOperatorClusterPolicies: clusterPolicies,
	})
}

func (p *GPUAcceleratorPlugin) ResolveVirtualizationConfig(
	_ context.Context,
	input VirtualizationConfigInput,
) (*accelerator.VirtualizationConfig, error) {
	devicePlugin := map[string]interface{}{
		"enabled":          true,
		"migStrategy":      "none",
		"deviceSplitCount": NvidiaGPUDefaultDeviceSplitCount,
		"nvidiaNodeSelector": map[string]interface{}{
			"gpu":                           nil,
			NvidiaGPUVirtualizationLabelKey: "true",
		},
	}
	configPatch := map[string]interface{}{"devicePlugin": devicePlugin}

	managed, runtimeClass, err := accelerator.NvidiaOperatorRuntime(input.GPUOperatorClusterPolicies)
	if err != nil {
		return nil, err
	}

	if managed {
		// Hami keeps ownership of the device-plugin and virtualization label;
		// only pass GPU Operator runtime compatibility through chart values.
		if accelerator.NvidiaNodesUseContainerd(input.Nodes) {
			devicePlugin["runtimeClassName"] = runtimeClass
		}

		if err := unstructured.SetNestedField(configPatch, accelerator.NvidiaGPUDiscoveryLabelValue,
			"devicePlugin", "nvidiaNodeSelector", accelerator.NvidiaGPUClientLabel); err != nil {
			return nil, err
		}

		devicePlugin["gpuOperatorToolkitReady"] = map[string]interface{}{
			"enabled":  true,
			"hostPath": accelerator.NvidiaValidationPath,
		}
	}

	blockingReasons := make([]string, 0)

	for _, policy := range input.GPUOperatorClusterPolicies {
		driverEnabled, found, err := unstructured.NestedBool(policy.Spec, "driver", "enabled")
		if !found || err != nil {
			driverEnabled = true
		}

		if driverEnabled {
			// GPU Operator managed drivers are usually mounted under this root.
			// HAMi device-plugin needs the same path to discover host devices.
			if err := unstructured.SetNestedField(configPatch, NvidiaGPUOperatorDriverRoot,
				"devicePlugin", "nvidiaDriverRoot"); err != nil {
				return nil, errors.Wrap(err, "failed to build NVIDIA GPU virtualization config patch")
			}
		}

		devicePluginEnabled, found, err := unstructured.NestedBool(policy.Spec, "devicePlugin", "enabled")
		if !found || err != nil {
			devicePluginEnabled = true
		}

		if devicePluginEnabled {
			blockingReasons = append(blockingReasons,
				"NVIDIA GPU Operator devicePlugin is enabled; disable it before enabling HAMi NVIDIA vGPU")
		}
	}

	candidateNodes := NvidiaVirtualizationCandidateNodes(input.Nodes)
	if len(candidateNodes) == 0 {
		blockingReasons = append(blockingReasons,
			"No NVIDIA GPU nodes available for vGPU virtualization")
	}

	return &accelerator.VirtualizationConfig{
		Supported:       true,
		BlockingReasons: blockingReasons,
		CandidateNodes:  candidateNodes,
		NodeScopeLabel: accelerator.VirtualizationNodeScopeLabel{
			Key:           NvidiaGPUVirtualizationLabelKey,
			EnabledValue:  "true",
			DisabledValue: "false",
		},
		ConfigPatch:    configPatch,
		Mode:           v1.AcceleratorVirtualizationModeCore,
		DefaultMode:    v1.AcceleratorVirtualizationModeCore,
		SupportedModes: nvidiaSupportedVirtualizationModes,
		SupportedResources: []string{
			v1.AcceleratorVirtualizationMemoryMiBKey,
			v1.AcceleratorVirtualizationCorePercentKey,
		},
	}, nil
}

// NvidiaVirtualizationCandidateNodes returns NVIDIA GPU nodes that can be
// labeled for HAMi vGPU support.
func NvidiaVirtualizationCandidateNodes(nodes []corev1.Node) []string {
	candidates := make([]string, 0)

	for _, node := range nodes {
		if node.Labels[NvidiaGPUDiscoveryLabelKey] == NvidiaGPUDiscoveryLabelValue {
			candidates = append(candidates, node.Name)
		}
	}

	return candidates
}

func kubernetesClientForVirtualizationConfig(cluster *v1.Cluster) (client.Reader, error) {
	if cluster == nil {
		return nil, errors.New("cluster is required to resolve NVIDIA GPU virtualization config")
	}

	ctrlClient, err := util.GetClientFromCluster(cluster)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create kubernetes client for NVIDIA GPU virtualization config")
	}

	return ctrlClient, nil
}

func listNvidiaVirtualizationNodes(ctx context.Context, ctrlClient client.Reader) ([]corev1.Node, error) {
	nodeList := &corev1.NodeList{}
	if err := ctrlClient.List(ctx, nodeList); err != nil {
		return nil, errors.Wrap(err, "failed to list nodes for NVIDIA GPU virtualization config")
	}

	return nodeList.Items, nil
}
