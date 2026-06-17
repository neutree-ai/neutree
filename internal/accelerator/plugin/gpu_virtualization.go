package plugin

import (
	"context"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func (p *GPUAcceleratorPlugin) ResolveClusterVirtualizationConfig(
	ctx context.Context,
	cluster *v1.Cluster,
) (*VirtualizationConfig, error) {
	ctrlClient, err := kubernetesClientForVirtualizationConfig(cluster)
	if err != nil {
		return nil, err
	}

	nodes, err := listNvidiaVirtualizationNodes(ctx, ctrlClient)
	if err != nil {
		return nil, err
	}

	clusterPolicies, err := listGPUOperatorClusterPolicies(ctx, ctrlClient)
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
) (*VirtualizationConfig, error) {
	configPatch := map[string]interface{}{}
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

func listGPUOperatorClusterPolicies(
	ctx context.Context,
	ctrlClient client.Reader,
) ([]GPUOperatorClusterPolicy, error) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("nvidia.com/v1")
	list.SetKind("ClusterPolicyList")

	if err := ctrlClient.List(ctx, list); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}

		return nil, errors.Wrap(err, "failed to list NVIDIA GPU Operator ClusterPolicy resources")
	}

	policies := make([]GPUOperatorClusterPolicy, 0, len(list.Items))

	for _, item := range list.Items {
		spec, _, err := unstructured.NestedMap(item.Object, "spec")
		if err != nil {
			return nil, errors.Wrapf(err, "failed to read NVIDIA GPU Operator ClusterPolicy %s spec", item.GetName())
		}

		policies = append(policies, GPUOperatorClusterPolicy{
			Name: item.GetName(),
			Spec: spec,
		})
	}

	return policies, nil
}
