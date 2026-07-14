package resource_test

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	resourceview "github.com/neutree-ai/neutree/internal/resource"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func endpointForTest(name string) *v1.Endpoint {
	return &v1.Endpoint{
		Metadata: &v1.Metadata{Name: name},
		Spec:     &v1.EndpointSpec{},
	}
}

func endpointForTestWithProduct(name, product string) *v1.Endpoint {
	endpoint := endpointForTest(name)
	endpoint.Spec.Resources = &v1.ResourceSpec{}
	endpoint.Spec.Resources.SetAcceleratorProduct(product)

	return endpoint
}

func k8sClusterForTest() *v1.Cluster {
	return &v1.Cluster{
		Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
	}
}

func TestK8sResourceClientListEndpointInstancesReportsEndpointProduct(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-1"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"raw-device-product","memory_mib":15360,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, nil)
	endpoint := endpointForTestWithProduct("chat", "NVIDIA-L20")

	instances, err := client.ListEndpointInstances(
		context.Background(),
		k8sClusterForTest(),
		endpoint,
	)

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Len(t, instances[0].Devices, 1)
	require.Equal(t, "NVIDIA-L20", instances[0].Devices[0].Product)

	resources, err := resourceview.NewResourceViewBuilder(client).
		BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)

	require.NoError(t, err)
	require.NotNil(t, resources)
	require.Len(t, resources.Replicas, 1)
	require.Len(t, resources.Replicas[0].Devices, 1)
	require.Equal(t, "NVIDIA-L20", resources.Replicas[0].Devices[0].Product)
	require.NotNil(t, resources.Summary)
	require.Contains(t, resources.Summary.Products, v1.AcceleratorProduct("NVIDIA-L20"))
	require.NotContains(t, resources.Summary.Products, v1.AcceleratorProduct("raw-device-product"))
	require.Equal(t, int64(15360), resources.Summary.Products["NVIDIA-L20"].MemoryMiB)
}

func k8sClusterNamespaceForTest() string {
	return util.ClusterNamespace(k8sClusterForTest())
}

func TestK8sResourceClientListEndpointInstancesWithNeutreeAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-1"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"Tesla-T4","vdevice_index":"0","memory_mib":15360,"used_memory_mib":4096,"core_units":100}
				]`,
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
		WithObjects(pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, nil)
	endpoint := endpointForTest("chat")

	instances, err := client.ListEndpointInstances(context.Background(), k8sClusterForTest(), endpoint)

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "chat-abc", instances[0].InstanceID)
	require.Equal(t, "chat-abc", instances[0].ReplicaID)
	require.Equal(t, "gpu-node", instances[0].NodeID)
	require.Len(t, instances[0].Devices, 1)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
	require.Equal(t, "Tesla-T4", instances[0].Devices[0].Product)
	require.Equal(t, "0", instances[0].Devices[0].VDeviceIndex)
	require.Equal(t, int64(15360), instances[0].Devices[0].MemoryMiB)
	require.Equal(t, int64(4096), instances[0].Devices[0].UsedMemoryMiB)
	require.Equal(t, int64(100), instances[0].Devices[0].CoreUnits)
	require.Equal(t, "gpu-node", instances[0].Devices[0].NodeID)

	resources, err := resourceview.NewResourceViewBuilder(client).
		BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)

	require.NoError(t, err)
	require.NotNil(t, resources)
	require.Len(t, resources.Replicas, 1)
	require.Len(t, resources.Replicas[0].Devices, 1)
	require.Equal(t, "Tesla-T4", resources.Replicas[0].Devices[0].Product)
}

func TestK8sResourceClientListEndpointInstancesSkipsMalformedNeutreeAllocation(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	goodPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-good",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-good"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}
				]`,
			},
		},
		Spec:   corev1.PodSpec{NodeName: "gpu-node"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}
	badPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-bad",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-bad"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `{bad-json`,
			},
		},
		Spec:   corev1.PodSpec{NodeName: "gpu-node"},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(goodPod, badPod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, nil)

	instances, err := client.ListEndpointInstances(context.Background(), k8sClusterForTest(), endpointForTest("chat"))

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "chat-good", instances[0].InstanceID)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
}

func TestK8sResourceClientListEndpointInstancesWithNeutreeAllocationWhenVirtualizationDisabled(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-1"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, nil)

	instances, err := client.ListEndpointInstances(context.Background(), k8sClusterForTest(), endpointForTest("chat"))

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Equal(t, "chat-abc", instances[0].InstanceID)
	require.Len(t, instances[0].Devices, 1)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
	require.Equal(t, "Tesla-T4", instances[0].Devices[0].Product)
	require.Equal(t, "gpu-node", instances[0].Devices[0].NodeID)
}

func TestK8sResourceClientListEndpointInstancesWithMultipleNeutreeAllocations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: k8sClusterNamespaceForTest(),
			UID:       types.UID("uid-1"),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":8192,"core_units":50},
					{"uuid":"GPU-2","product":"Tesla-T4","memory_mib":8192,"core_units":50}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
			Containers: []corev1.Container{
				{
					Name: "main",
					Resources: corev1.ResourceRequirements{
						Limits: corev1.ResourceList{
							plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("2"),
							plugin.NvidiaGPUCoreResource:       k8sresource.MustParse("100"),
						},
						Requests: corev1.ResourceList{
							plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("2"),
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
		WithObjects(pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, nil)

	instances, err := client.ListEndpointInstances(context.Background(), k8sClusterForTest(), endpointForTest("chat"))

	require.NoError(t, err)
	require.Len(t, instances, 1)
	require.Len(t, instances[0].Devices, 2)
	require.Equal(t, "GPU-1", instances[0].Devices[0].UUID)
	require.Equal(t, int64(8192), instances[0].Devices[0].MemoryMiB)
	require.Equal(t, int64(50), instances[0].Devices[0].CoreUnits)
	require.Equal(t, "GPU-2", instances[0].Devices[1].UUID)
	require.Equal(t, int64(8192), instances[0].Devices[1].MemoryMiB)
	require.Equal(t, int64(50), instances[0].Devices[1].CoreUnits)
}

func TestK8sResourceClientListNodesUsesNeutreeDeviceAnnotations(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
				plugin.NvidiaGPUCountResource:             "2",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
					{"uuid":"GPU-1","product_model":"Annotation-T4","memory_mib":15360,"healthy":true,"minor_number":3},
					{"uuid":"GPU-2","product_model":"Annotation-T4","memory_mib":15360,"healthy":true,"minor_number":0}
				]`,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                 k8sresource.MustParse("8"),
				corev1.ResourceMemory:              k8sresource.MustParse("32Gi"),
				plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("200"),
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "infer-1",
			Namespace: "default",
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 2)
	require.Equal(t, "Tesla-T4", nodes[0].Status.Devices[0].Product)
	require.Equal(t, "Tesla-T4", nodes[0].Status.Devices[1].Product)
	require.Equal(t, int64(0), nodes[0].Status.Devices[0].Available.MemoryMiB)
	require.Equal(t, int64(15360), nodes[0].Status.Devices[1].Available.MemoryMiB)
	require.NotNil(t, nodes[0].Status.Devices[0].Order)
	require.Equal(t, 1, *nodes[0].Status.Devices[0].Order)
	require.NotNil(t, nodes[0].Status.Devices[1].Order)
	require.Equal(t, 0, *nodes[0].Status.Devices[1].Order)
	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(30720), allocatable.Products["Tesla-T4"].Virtualization.MemoryMiB)
	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["Tesla-T4"])
	require.Equal(t, float64(1), available.Products["Tesla-T4"].Quantity)
	require.Equal(t, float64(15360), available.Products["Tesla-T4"].Virtualization.MemoryMiB)
}

func TestK8sResourceClientListNodesUsesNeutreeDeviceAnnotationsForTimeSlicingQuantity(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA-L20",
				plugin.NvidiaGPUMemoryNodeLabelKey:        "46068",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
					{"uuid":"GPU-1","product_model":"Annotation-L20","product_name":"NVIDIA L20","memory_mib":46068,"healthy":true,"minor_number":0},
					{"uuid":"GPU-2","product_model":"Annotation-L20","product_name":"NVIDIA L20","memory_mib":46068,"healthy":true,"minor_number":1}
				]`,
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
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "infer-1",
			Namespace: "default",
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":46068,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 2)
	require.Equal(t, "NVIDIA-L20", nodes[0].Status.Devices[0].Product)
	require.Equal(t, "NVIDIA-L20", nodes[0].Status.Devices[1].Product)
	require.Equal(t, int64(0), nodes[0].Status.Devices[0].Available.MemoryMiB)
	require.Equal(t, int64(46068), nodes[0].Status.Devices[1].Available.MemoryMiB)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA-L20"])
	require.Equal(t, float64(2), allocatable.Products["NVIDIA-L20"].Quantity)
	require.NotContains(t, allocatable.ProductGroups, v1.AcceleratorProduct("Annotation-L20"))
	require.NotContains(t, allocatable.Products, v1.AcceleratorProduct("Annotation-L20"))

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["NVIDIA-L20"])
	require.Equal(t, float64(1), available.Products["NVIDIA-L20"].Quantity)
}

func TestK8sResourceClientListNodesUsesNeutreeDeviceAvailabilityForAvailableGPUQuantity(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA-L20",
				plugin.NvidiaGPUCountResource:             "2",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
					{"uuid":"GPU-1","memory_mib":46068,"healthy":true,"minor_number":0},
					{"uuid":"GPU-2","memory_mib":46068,"healthy":true,"minor_number":1}
				]`,
			},
		},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:                 k8sresource.MustParse("8"),
				corev1.ResourceMemory:              k8sresource.MustParse("32Gi"),
				plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("200"),
			},
		},
	}
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "infer-1",
			Namespace: "default",
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":46068,"core_units":100},
					{"uuid":"GPU-2","product":"NVIDIA-L20","memory_mib":46068,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node",
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
		},
	}

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node, pod).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 2)
	require.Equal(t, int64(0), nodes[0].Status.Devices[0].Available.MemoryMiB)
	require.Equal(t, int64(0), nodes[0].Status.Devices[0].Available.CoreUnits)
	require.Equal(t, int64(0), nodes[0].Status.Devices[1].Available.MemoryMiB)
	require.Equal(t, int64(0), nodes[0].Status.Devices[1].Available.CoreUnits)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA-L20"])
	require.Equal(t, float64(2), allocatable.Products["NVIDIA-L20"].Quantity)

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(0), available.Quantity)
	require.Equal(t, float64(0), available.ProductGroups["NVIDIA-L20"])
	require.Equal(t, float64(0), available.Products["NVIDIA-L20"].Quantity)
}

func TestK8sResourceClientListNodesEnhancesBaseResourcesWithoutGPUCountLabel(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "Tesla-T4",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
					{"uuid":"GPU-1","memory_mib":15360,"healthy":true}
				]`,
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
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 1)
	require.Equal(t, "GPU-1", nodes[0].Status.Devices[0].UUID)
	require.Equal(t, "Tesla-T4", nodes[0].Status.Devices[0].Product)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), allocatable.Quantity)
	require.Equal(t, float64(1), allocatable.ProductGroups["Tesla-T4"])
	require.Equal(t, float64(15360), allocatable.Products["Tesla-T4"].Virtualization.MemoryMiB)
}

func TestK8sResourceClientListNodesUsesStandardParserWithoutNeutreeDeviceAnnotations(t *testing.T) {
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
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

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

func TestK8sResourceClientListNodesSubtractsGPURequestsWhenGPUCountLabelExists(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA-L20",
				plugin.NvidiaGPUCountResource:             "2",
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
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA-L20"])

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1), available.Quantity)
	require.Equal(t, float64(1), available.ProductGroups["NVIDIA-L20"])
}

func TestK8sResourceClientListNodesFallsBackToBaseResourcesWhenNeutreeDeviceAnnotationMalformed(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	node := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name: "gpu-node",
			Labels: map[string]string{
				plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA_A100",
				plugin.NvidiaGPUMemoryNodeLabelKey:        "81920",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorDevicesAnnotation: `{bad-json`,
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

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(node).
		Build()
	client := resourceview.NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Empty(t, nodes[0].Status.Devices)

	allocatable := nodes[0].Status.Allocatable.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(2), allocatable.Quantity)
	require.Equal(t, float64(2), allocatable.ProductGroups["NVIDIA_A100"])
	require.Equal(t, float64(81920),
		nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU].Products["NVIDIA_A100"].MemoryTotalMiB)
}
