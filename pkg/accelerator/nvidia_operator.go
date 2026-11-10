package accelerator

import (
	"context"
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type GPUOperatorClusterPolicy struct {
	Name   string
	Spec   map[string]interface{}
	Labels map[string]string
}

func ListGPUOperatorClusterPolicies(ctx context.Context, reader client.Reader) ([]GPUOperatorClusterPolicy, error) {
	list := &unstructured.UnstructuredList{}
	list.SetAPIVersion("nvidia.com/v1")
	list.SetKind("ClusterPolicyList")

	if err := reader.List(ctx, list); err != nil {
		if apierrors.IsNotFound(err) || meta.IsNoMatchError(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("list NVIDIA ClusterPolicy: %w", err)
	}

	policies := make([]GPUOperatorClusterPolicy, 0, len(list.Items))

	for _, item := range list.Items {
		spec, _, err := unstructured.NestedMap(item.Object, "spec")
		if err != nil {
			return nil, fmt.Errorf("read NVIDIA ClusterPolicy %s: %w", item.GetName(), err)
		}

		policies = append(policies, GPUOperatorClusterPolicy{Name: item.GetName(), Spec: spec, Labels: item.GetLabels()})
	}

	return policies, nil
}

// NvidiaOperatorRuntime requires both explicit management flags; a missing
// operand is not evidence that GPU Operator owns the dependency.
func NvidiaOperatorRuntime(policies []GPUOperatorClusterPolicy) (managed bool, runtimeClass string, err error) {
	for _, policy := range policies {
		driver, _, err := unstructured.NestedBool(policy.Spec, "driver", "enabled")
		if err != nil {
			return false, "", fmt.Errorf("read ClusterPolicy %s driver.enabled: %w", policy.Name, err)
		}

		toolkit, _, err := unstructured.NestedBool(policy.Spec, "toolkit", "enabled")
		if err != nil {
			return false, "", fmt.Errorf("read ClusterPolicy %s toolkit.enabled: %w", policy.Name, err)
		}

		if !driver || !toolkit {
			continue
		}

		runtimeClass, _, err = unstructured.NestedString(policy.Spec, "operator", "runtimeClass")
		if err != nil {
			return false, "", fmt.Errorf("read ClusterPolicy %s operator.runtimeClass: %w", policy.Name, err)
		}

		if runtimeClass == "" {
			runtimeClass = NvidiaDefaultRuntimeClass
		}

		return true, runtimeClass, nil
	}

	return false, "", nil
}

// NvidiaNodesUseContainerd uses stable hardware discovery rather than client
// admission or allocatable resources, which disappear while drivers recover.
func NvidiaNodesUseContainerd(nodes []corev1.Node) bool {
	foundGPU := false

	for _, node := range nodes {
		if node.Labels[NvidiaGPUDiscoveryLabelKey] != NvidiaGPUDiscoveryLabelValue {
			continue
		}

		foundGPU = true

		runtime, version, found := strings.Cut(node.Status.NodeInfo.ContainerRuntimeVersion, "://")
		if !found || runtime != "containerd" || version == "" {
			return false
		}
	}

	return foundGPU
}
