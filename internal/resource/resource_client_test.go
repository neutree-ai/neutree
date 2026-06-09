package resource_test

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestK8sResourceClientListEndpointInstancesWithHAMiAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
				plugin.NvidiaGPUVirtualizationLabelKey:    "true",
			},
			Annotations: map[string]string{
				plugin.HAMiNodeNvidiaRegisterAnnotation: `[
					{"id":"GPU-1","count":100,"devmem":15360,"devcore":100,"type":"NVIDIA-Tesla T4","health":true}
				]`,
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: "default",
			UID:       types.UID("uid-1"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				plugin.HAMiVGPUDevicesAllocatedAnnotation: ";GPU-1,NVIDIA,15360,100:;",
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("1"),
							plugin.NvidiaGPUCoreResource:       k8sresource.MustParse("100"),
						},
						Requests: corev1.ResourceList{
							plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("1"),
							plugin.NvidiaGPUCoreResource:       k8sresource.MustParse("100"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	instances, err := client.ListEndpointInstances(context.Background(), resourceview.ListEndpointInstancesOptions{
		EndpointName:                     "chat",
		Namespace:                        "default",
		AcceleratorVirtualizationEnabled: true,
	})

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "chat-abc", instances[0].InstanceID)
	require.Equal(t, "chat-abc", instances[0].ReplicaID)
	require.Equal(t, "gpu-node", instances[0].NodeID)
	require.Len(t, instances[0].Devices, 1)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
	require.Equal(t, "Tesla-T4", instances[0].Devices[0].Product)
	require.Equal(t, int64(15360), instances[0].Devices[0].MemoryMiB)
	require.Equal(t, int64(100), instances[0].Devices[0].CoreUnits)
	require.Equal(t, "gpu-node", instances[0].Devices[0].NodeID)
}

func TestK8sResourceClientListNodesFallsBackToStandardParserWhenVirtualizationEnabled(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
				plugin.NvidiaGPUMemoryNodeLabelKey:        "81920",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                 k8sresource.MustParse("8"),
				corev1.ResourceMemory:              k8sresource.MustParse("32Gi"),
				plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("2"),
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "infer-1",
			Namespace: "default",
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{
							plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("1"),
						},
					},
				},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), resourceview.ListNodesOptions{
		AcceleratorVirtualizationEnabled: true,
	})

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Empty(t, nodes[0].Status.Devices)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA_A100"])

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(81920),
		nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_A100"].MemoryTotalMiB)
}

func TestK8sResourceClientListNodesDoesNotExposeHAMiSlotsWithoutRegisteredDevices(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
				plugin.NvidiaGPUVirtualizationLabelKey:    "true",
				plugin.NvidiaGPUCountResource:             "2",
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                 k8sresource.MustParse("8"),
				corev1.ResourceMemory:              k8sresource.MustParse("32Gi"),
				plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("20"),
			},
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceview.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), resourceview.ListNodesOptions{
		AcceleratorVirtualizationEnabled: true,
	})

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Empty(t, nodes[0].Status.Devices)
	require.Empty(t, nodes[0].Status.Allocatable.AcceleratorGroups)
	require.Empty(t, nodes[0].Status.Available.AcceleratorGroups)
	require.Equal(t, float64(8), nodes[0].Status.Allocatable.CPU)
	require.Equal(t, float64(32), nodes[0].Status.Allocatable.Memory)
}
