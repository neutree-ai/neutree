package resource

import (
	"context"
	"fmt"
	"io"
	"strings"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/accelerator/resourceparser"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	k8sresource "k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
	client := NewK8sResourceClient(ctrClient, nil)
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

	resources, err := NewResourceViewBuilder(client).
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
	client := NewK8sResourceClient(ctrClient, nil)
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

	resources, err := NewResourceViewBuilder(client).
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
	client := NewK8sResourceClient(ctrClient, nil)

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
	client := NewK8sResourceClient(ctrClient, nil)

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
	client := NewK8sResourceClient(ctrClient, nil)

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
	cases := []struct {
		name                  string
		healthy               bool
		allocation            string
		wantAvailable         v1.DeviceResourcePool
		wantAvailableQuantity float64
	}{
		{
			name:                  "healthy allocated",
			healthy:               true,
			allocation:            `[{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}]`,
			wantAvailable:         v1.DeviceResourcePool{},
			wantAvailableQuantity: 1,
		},
		{
			name:                  "unhealthy allocated",
			allocation:            `[{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":15360,"core_units":100}]`,
			wantAvailable:         v1.DeviceResourcePool{},
			wantAvailableQuantity: 1,
		},
		{
			name:                  "healthy idle",
			healthy:               true,
			wantAvailable:         v1.DeviceResourcePool{MemoryMiB: 15360, CoreUnits: 100},
			wantAvailableQuantity: 2,
		},
		{
			name:                  "unhealthy idle",
			wantAvailable:         v1.DeviceResourcePool{MemoryMiB: 15360, CoreUnits: 100},
			wantAvailableQuantity: 2,
		},
		{
			name:                  "healthy partially allocated",
			healthy:               true,
			allocation:            `[{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":4096,"core_units":25}]`,
			wantAvailable:         v1.DeviceResourcePool{MemoryMiB: 11264, CoreUnits: 75},
			wantAvailableQuantity: 1 + float64(11264)/15360,
		},
		{
			name:                  "unhealthy partially allocated",
			allocation:            `[{"uuid":"GPU-1","product":"Tesla-T4","memory_mib":4096,"core_units":25}]`,
			wantAvailable:         v1.DeviceResourcePool{MemoryMiB: 11264, CoreUnits: 75},
			wantAvailableQuantity: 1 + float64(11264)/15360,
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
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
						resourceparser.NeutreeAcceleratorDevicesAnnotation: fmt.Sprintf(`[
							{"uuid":"GPU-1","product_model":"Annotation-T4","memory_mib":15360,"healthy":%t,"minor_number":3},
							{"uuid":"GPU-2","product_model":"Annotation-T4","memory_mib":15360,"healthy":%t,"minor_number":0}
						]`, testCase.healthy, testCase.healthy),
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
						resourceparser.NeutreeAcceleratorAllocationsAnnotation: testCase.allocation,
					},
				},
				Spec:   corev1.PodSpec{NodeName: "gpu-node"},
				Status: corev1.PodStatus{Phase: corev1.PodRunning},
			}

			ctrClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(node, pod).Build()
			client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
				string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
			})

			nodes, err := client.ListNodes(context.Background(), nil)

			require.NoError(t, err)
			require.Len(t, nodes, 1)
			require.Len(t, nodes[0].Status.Devices, 2)
			device := nodes[0].Status.Devices[0]
			assert.Equal(t, "GPU-1", device.UUID)
			assert.Equal(t, "Tesla-T4", device.Product)
			assert.Equal(t, testCase.healthy, device.Health)
			assert.Equal(t, &v1.DeviceResourcePool{MemoryMiB: 15360, CoreUnits: 100}, device.Allocatable)
			assert.Equal(t, &testCase.wantAvailable, device.Available)
			assert.Equal(t, "GPU-2", nodes[0].Status.Devices[1].UUID)
			assert.Equal(t, "Tesla-T4", nodes[0].Status.Devices[1].Product)
			assert.Equal(t, testCase.healthy, nodes[0].Status.Devices[1].Health)
			assert.Equal(t, &v1.DeviceResourcePool{MemoryMiB: 15360, CoreUnits: 100}, nodes[0].Status.Devices[1].Available)
			require.NotNil(t, device.Order)
			assert.Equal(t, 1, *device.Order)
			require.NotNil(t, nodes[0].Status.Devices[1].Order)
			assert.Equal(t, 0, *nodes[0].Status.Devices[1].Order)

			for index, info := range []*v1.ResourceInfo{nodes[0].Status.Allocatable, nodes[0].Status.Available} {
				wantQuantity := float64(2)
				wantVirtualization := &v1.AcceleratorVirtualizationResource{MemoryMiB: 30720, CoreUnits: 200}
				if index == 1 {
					wantQuantity = testCase.wantAvailableQuantity
					wantVirtualization = &v1.AcceleratorVirtualizationResource{
						MemoryMiB: 15360 + float64(testCase.wantAvailable.MemoryMiB),
						CoreUnits: 100 + float64(testCase.wantAvailable.CoreUnits),
					}
				}
				group := info.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
				require.NotNil(t, group)
				assert.Equal(t, wantQuantity, group.Quantity)
				assert.Equal(t, wantQuantity, group.ProductGroups["Tesla-T4"])
				product := group.Products["Tesla-T4"]
				require.NotNil(t, product)
				assert.Equal(t, wantQuantity, product.Quantity)
				assert.Equal(t, wantVirtualization, product.Virtualization)
			}
			metadata := nodes[0].AcceleratorMetadata[v1.AcceleratorTypeNVIDIAGPU]
			require.NotNil(t, metadata)
			require.NotNil(t, metadata.Products["Tesla-T4"])
			assert.Equal(t, float64(15360), metadata.Products["Tesla-T4"].MemoryTotalMiB)
		})
	}
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
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

func TestK8sResourceClientListNodesPartiallyAllocatedDeviceContributesEquivalentAvailableCapacity(t *testing.T) {
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
					{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":23034,"core_units":50}
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
		string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
	})

	nodes, err := client.ListNodes(context.Background(), nil)

	require.NoError(t, err)
	require.Len(t, nodes, 1)
	require.Len(t, nodes[0].Status.Devices, 2)

	// GPU-1 is partially allocated (half memory + half cores), so it contributes
	// half an equivalent card while GPU-2 contributes one full equivalent card.
	require.Equal(t, int64(23034), nodes[0].Status.Devices[0].Available.MemoryMiB)
	require.Equal(t, int64(50), nodes[0].Status.Devices[0].Available.CoreUnits)

	available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
	require.Equal(t, float64(1.5), available.Quantity)
	require.Equal(t, float64(1.5), available.ProductGroups["NVIDIA-L20"])
	require.Equal(t, float64(1.5), available.Products["NVIDIA-L20"].Quantity)
	// Remaining capacity pool aggregates GPU-1 (23034/50) + GPU-2 (46068/100).
	require.Equal(t, float64(69102), available.Products["NVIDIA-L20"].Virtualization.MemoryMiB)
	require.Equal(t, float64(150), available.Products["NVIDIA-L20"].Virtualization.CoreUnits)
}

func TestK8sResourceClientListNodesAvailableEquivalentCapacityScenarios(t *testing.T) {
	// A healthy device contributes its smaller remaining memory/core ratio.
	// Cover unallocated, memory-only, compute-only, asymmetric, single-dimension
	// exhausted, and fully exhausted allocations from NEU-608.
	const allocMemoryMiB = int64(46068)
	const allocCoreUnits = int64(100)

	testCases := []struct {
		name              string
		allocation        string // JSON for neutree.ai/accelerator-allocations annotation
		wantAvailable     float64
		wantPoolMemoryMiB float64
		wantPoolCoreUnits float64
		wantPoolNil       bool
	}{
		{
			name:              "unallocated card contributes one",
			allocation:        "",
			wantAvailable:     1,
			wantPoolMemoryMiB: float64(allocMemoryMiB),
			wantPoolCoreUnits: float64(allocCoreUnits),
		},
		{
			name:              "memory-only allocation contributes remaining memory ratio",
			allocation:        `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":23034,"core_units":0}]`,
			wantAvailable:     0.5,
			wantPoolMemoryMiB: 23034,
			wantPoolCoreUnits: float64(allocCoreUnits),
		},
		{
			name:              "compute-only allocation contributes remaining compute ratio",
			allocation:        `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":0,"core_units":50}]`,
			wantAvailable:     0.5,
			wantPoolMemoryMiB: float64(allocMemoryMiB),
			wantPoolCoreUnits: 50,
		},
		{
			name:              "matching partial allocation contributes one half",
			allocation:        `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":23034,"core_units":50}]`,
			wantAvailable:     0.5,
			wantPoolMemoryMiB: 23034,
			wantPoolCoreUnits: 50,
		},
		{
			name:              "smaller compute ratio limits capacity",
			allocation:        `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":11517,"core_units":75}]`,
			wantAvailable:     0.25,
			wantPoolMemoryMiB: 34551,
			wantPoolCoreUnits: 25,
		},
		{
			name:          "exhausted memory does not contribute a residual pool",
			allocation:    `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":46068,"core_units":0}]`,
			wantAvailable: 0,
			wantPoolNil:   true,
		},
		{
			name:          "exhausted compute does not contribute a residual pool",
			allocation:    `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":0,"core_units":100}]`,
			wantAvailable: 0,
			wantPoolNil:   true,
		},
		{
			name:              "fully exhausted card is not available",
			allocation:        `[{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":46068,"core_units":100}]`,
			wantAvailable:     0,
			wantPoolMemoryMiB: 0,
			wantPoolCoreUnits: 0,
			wantPoolNil:       true,
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			require.NoError(t, corev1.AddToScheme(scheme))

			node := &corev1.Node{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu-node",
					Labels: map[string]string{
						plugin.NvidiaGPUKubernetesNodeSelectorKey: "NVIDIA-L20",
						plugin.NvidiaGPUCountResource:             "1",
					},
					Annotations: map[string]string{
						resourceparser.NeutreeAcceleratorDevicesAnnotation: `[
							{"uuid":"GPU-1","memory_mib":46068,"healthy":true,"minor_number":0}
						]`,
					},
				},
				Status: corev1.NodeStatus{
					Allocatable: corev1.ResourceList{
						corev1.ResourceCPU:                 k8sresource.MustParse("8"),
						corev1.ResourceMemory:              k8sresource.MustParse("32Gi"),
						plugin.NvidiaGPUKubernetesResource: k8sresource.MustParse("100"),
					},
				},
			}

			objects := []client.Object{node}
			if tc.allocation != "" {
				objects = append(objects, &corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "infer-1",
						Namespace: "default",
						Annotations: map[string]string{
							resourceparser.NeutreeAcceleratorAllocationsAnnotation: tc.allocation,
						},
					},
					Spec:   corev1.PodSpec{NodeName: "gpu-node"},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				})
			}

			ctrClient := fake.NewClientBuilder().
				WithScheme(scheme).
				WithObjects(objects...).
				Build()
			client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
				string(v1.AcceleratorTypeNVIDIAGPU): &plugin.GPUResourceParser{},
			})

			nodes, err := client.ListNodes(context.Background(), nil)

			require.NoError(t, err)
			require.Len(t, nodes, 1)
			require.Len(t, nodes[0].Status.Devices, 1)

			available := nodes[0].Status.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU]
			require.NotNil(t, available)
			assert.Equal(t, tc.wantAvailable, available.Quantity)
			assert.Equal(t, tc.wantAvailable, available.ProductGroups["NVIDIA-L20"])
			assert.Equal(t, tc.wantAvailable, available.Products["NVIDIA-L20"].Quantity)
			if tc.wantPoolNil {
				assert.Nil(t, available.Products["NVIDIA-L20"].Virtualization)
				return
			}
			assert.Equal(t, tc.wantPoolMemoryMiB, available.Products["NVIDIA-L20"].Virtualization.MemoryMiB)
			assert.Equal(t, tc.wantPoolCoreUnits, available.Products["NVIDIA-L20"].Virtualization.CoreUnits)
		})
	}
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
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
	client := NewK8sResourceClient(ctrClient, map[string]resourceparser.ResourceParser{
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

type fakePodLogClient struct {
	calls   int
	logs    string
	getLogs func(context.Context) (io.ReadCloser, error)
}

func (f *fakePodLogClient) GetPod(context.Context, *v1.Cluster, string, string) (*corev1.Pod, error) {
	return nil, nil
}

func (f *fakePodLogClient) GetPodLogs(ctx context.Context, _ *v1.Cluster, _, _ string, opts *corev1.PodLogOptions) (io.ReadCloser, error) {
	f.calls++
	if f.getLogs != nil {
		return f.getLogs(ctx)
	}
	return io.NopCloser(strings.NewReader(f.logs)), nil
}

func TestK8sResourceClientParsesAndCachesVLLMStartupMemory(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "chat-abc",
			Namespace: k8sClusterNamespaceForTest(),
			Labels: map[string]string{
				"app":      "inference",
				"endpoint": "chat",
			},
			Annotations: map[string]string{
				resourceparser.NeutreeAcceleratorAllocationsAnnotation: `[
					{"uuid":"GPU-1","product":"NVIDIA-L20","memory_mib":46080,"core_units":100}
				]`,
			},
		},
		Spec: corev1.PodSpec{
			NodeName: "gpu-node-1",
			Containers: []corev1.Container{
				{Name: "vllm"},
			},
		},
		Status: corev1.PodStatus{
			Phase: corev1.PodRunning,
			ContainerStatuses: []corev1.ContainerStatus{
				{
					Name:        "vllm",
					Ready:       true,
					ContainerID: "containerd://cid-1",
				},
			},
		},
	}

	ctrClient := fake.NewClientBuilder().WithScheme(scheme).WithObjects(pod).Build()
	logClient := &fakePodLogClient{
		logs: "INFO 10-22 10:00:00 [gpu_model_runner.py:123] Model loading took 14.5000 GiB and 3.21 seconds\n" +
			"INFO 10-22 10:00:02 [gpu_worker.py:234] Available KV cache memory: 23.25 GiB\n",
	}
	resClient := NewK8sResourceClient(ctrClient, nil)
	resClient.podLogClient = logClient
	builder := NewResourceViewBuilder(resClient)

	endpoint := endpointForTestWithProduct("chat", "NVIDIA-L20")
	endpoint.Spec.Engine = &v1.EndpointEngineSpec{Engine: "vllm"}
	status, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.NotNil(t, status)
	require.Len(t, status.Replicas, 1)
	require.Equal(t, 1, logClient.calls)
	require.Equal(t, "containerd://cid-1", status.Replicas[0].ContainerID)
	require.NotNil(t, status.Replicas[0].ModelWeightsMiB)
	require.InDelta(t, 14848.0, *status.Replicas[0].ModelWeightsMiB, 0.01)
	require.NotNil(t, status.Replicas[0].KVCacheCapacityMiB)
	require.InDelta(t, 23808.0, *status.Replicas[0].KVCacheCapacityMiB, 0.01)

	// Subsequent reconcile with the same containerID reuses cached values without calling GetPodLogs.
	endpoint.Status = &v1.EndpointStatus{Resources: status}
	status2, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Equal(t, 1, logClient.calls)
	require.Equal(t, status.Replicas[0].ModelWeightsMiB, status2.Replicas[0].ModelWeightsMiB)
	require.Equal(t, status.Replicas[0].KVCacheCapacityMiB, status2.Replicas[0].KVCacheCapacityMiB)

	for _, key := range []string{"num_gpu_blocks_override", "num-gpu-blocks-override"} {
		endpoint.Spec.Variables = map[string]interface{}{"engine_args": map[string]interface{}{key: 128}}
		for _, cached := range []bool{true, false} {
			endpoint.Status.Resources = status
			if !cached {
				endpoint.Status.Resources = nil
			}
			got, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
			require.NoError(t, err)
			require.NotNil(t, got.Replicas[0].ModelWeightsMiB)
			require.Nil(t, got.Replicas[0].KVCacheCapacityMiB, key)
		}
	}
	endpoint.Spec.Variables = nil
	// A successful read with no matching startup lines is cached for this container.
	endpoint.Status.Resources = nil
	logClient.logs = "starting up\n"
	calls := logClient.calls
	empty, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+1, logClient.calls)
	require.Equal(t, "containerd://cid-1", empty.Replicas[0].ContainerID)
	endpoint.Status.Resources = empty
	logClient.logs = "Model loading took 6 GiB\nAvailable KV cache memory: 8 GiB\n" + strings.Repeat("x", int(startupLogLimitBytes))
	stillEmpty, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+1, logClient.calls)
	require.Nil(t, stillEmpty.Replicas[0].ModelWeightsMiB)
	require.Nil(t, stillEmpty.Replicas[0].KVCacheCapacityMiB)

	// A new container must be read even when the previous container had no data.
	pod.Status.ContainerStatuses[0].ContainerID = "containerd://cid-2"
	require.NoError(t, ctrClient.Status().Update(context.Background(), pod))
	filled, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+2, logClient.calls)
	require.Equal(t, "containerd://cid-2", filled.Replicas[0].ContainerID)
	require.InDelta(t, 6144.0, *filled.Replicas[0].ModelWeightsMiB, 0.01)
	require.InDelta(t, 8192.0, *filled.Replicas[0].KVCacheCapacityMiB, 0.01)
	endpoint.Status.Resources = nil
	logClient.getLogs = func(ctx context.Context) (io.ReadCloser, error) {
		deadline, ok := ctx.Deadline()
		require.True(t, ok)
		require.LessOrEqual(t, time.Until(deadline), startupLogTimeout)
		return &blockedLogReader{ctx: ctx}, nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	got, err := builder.BuildEndpointResources(ctx, k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Nil(t, got.Replicas[0].ModelWeightsMiB)
	require.ErrorIs(t, ctx.Err(), context.DeadlineExceeded)
	require.Empty(t, got.Replicas[0].ContainerID)

	// Request failures also leave collection incomplete.
	endpoint.Status.Resources = got
	logClient.getLogs = func(context.Context) (io.ReadCloser, error) {
		return nil, context.Canceled
	}
	failed, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Empty(t, failed.Replicas[0].ContainerID)

	// Failed reads are retried, rather than cached as a successful empty result.
	endpoint.Status.Resources = failed
	logClient.getLogs = nil
	calls = logClient.calls
	retried, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
	require.NoError(t, err)
	require.Equal(t, calls+1, logClient.calls)
	require.NotNil(t, retried.Replicas[0].ModelWeightsMiB)

	for _, engine := range []string{"sglang", "llama-cpp", "custom"} {
		endpoint.Spec.Engine.Engine = engine
		endpoint.Status.Resources = nil
		calls = logClient.calls
		result, err := builder.BuildEndpointResources(context.Background(), k8sClusterForTest(), endpoint)
		require.NoError(t, err)
		require.Equal(t, calls, logClient.calls, engine)
		require.Empty(t, result.Replicas[0].ContainerID)
		require.Nil(t, result.Replicas[0].ModelWeightsMiB)
	}

}

type blockedLogReader struct{ ctx context.Context }

func (r *blockedLogReader) Read([]byte) (int, error) {
	<-r.ctx.Done()
	return 0, r.ctx.Err()
}
func (r *blockedLogReader) Close() error { return nil }

func TestVLLMStartupMemory(t *testing.T) {
	for _, tc := range []struct {
		name        string
		logs        string
		weights, kv float64
	}{
		{"plain", "Model loading took 6 GiB\nAvailable KV cache memory: 8 GiB\n", 6, 8},
		{"mixed prefixes and different budgets", "Model loading took 6 GiB\nAvailable KV cache memory: 12.10 GiB\n(Worker_TP1 pid=11) Model loading took 7 GiB\n(Worker_TP1 pid=11) Available KV cache memory: 12.35 GiB\n", 7, 12.10},
		{"shared nested prefix", "(outer) (EngineCore pid=1) Model loading took 7 GiB\n(outer) (EngineCore pid=1) Model loading took 6 GiB\n(outer) (EngineCore pid=1) Available KV cache memory: 9 GiB\n(outer) (EngineCore pid=1) Available KV cache memory: 8 GiB\n", 7, 8},
		{"weights only", "Model loading took 6 GiB\n", 6, 0},
		{"no positive values", "Model loading took 0 GiB\nAvailable KV cache memory: 0 GiB\n", 0, 0},
		{"empty", "", 0, 0},
		{"long running log", "Model loading took 6 GiB\nAvailable KV cache memory: 8 GiB\n" + strings.Repeat("x", int(startupLogLimitBytes)), 6, 8},
	} {
		t.Run(tc.name, func(t *testing.T) {
			weights, kv := parseVLLMStartupMemoryMiB(tc.logs)
			for i, got := range []*float64{weights, kv} {
				want := []float64{tc.weights, tc.kv}[i]
				if want == 0 {
					require.Nil(t, got)
				} else {
					require.NotNil(t, got)
					require.InDelta(t, want*1024, *got, 0.01)
				}
			}
		})
	}
}
