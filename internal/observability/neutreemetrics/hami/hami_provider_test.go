package hami

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestEndpointGPUUsagesFromHAMiMetrics(t *testing.T) {
	raw := `
hami_vgpu_memory_limit_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 8589934592
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 4294967296
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a",device_name="NVIDIA_A100"} 0.75
hami_vgpu_memory_used_bytes{namespace="default",pod="sidecar",container="debug",device_uuid="GPU-ignored",vdevice_index="0",node="node-a"} 1024
`
	pods := map[podKey]podIdentity{
		{namespace: "default", name: "chat-abc"}: {
			workspace: "team-a",
			cluster:   "k8s-a",
			endpoint:  "chat",
			node:      "node-a",
		},
	}

	usages := endpointGPUUsagesFromHAMiMetrics(raw, pods)

	require.Len(t, usages, 1)
	assert.Equal(t, "team-a", usages[0].Workspace)
	assert.Equal(t, "k8s-a", usages[0].Cluster)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "chat-abc", usages[0].InstanceID)
	assert.Equal(t, "chat-abc", usages[0].ReplicaID)
	assert.Equal(t, "node-a", usages[0].NodeID)
	assert.Equal(t, "engine", usages[0].Container)
	assert.Equal(t, "GPU-abc", usages[0].GPUUUID)
	assert.Equal(t, "0", usages[0].VDeviceIndex)
	assert.Equal(t, "NVIDIA_A100", usages[0].Product)
	require.NotNil(t, usages[0].MemoryAllocatedBytes)
	assert.Equal(t, 8589934592.0, *usages[0].MemoryAllocatedBytes)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 4294967296.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}

func TestKubernetesProviderScrapesLocalHAMiMonitor(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "chat-abc",
			Labels: map[string]string{
				"app":                              "inference",
				"endpoint":                         "chat",
				"workspace":                        "team-a",
				v1.NeutreeClusterLabelKey:          "k8s-a",
				v1.NeutreeClusterWorkspaceLabelKey: "team-a",
			},
			Annotations: map[string]string{
				hamiVGPUDevicesAllocated: ";GPU-abc,NVIDIA,8192,50:;",
			},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	monitorPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "hami-device-plugin-node-a",
			Labels: map[string]string{
				"app.kubernetes.io/component": "hami-device-plugin",
			},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{PodIP: "10.0.0.2"},
	}
	remoteMonitorPod := monitorPod.DeepCopy()
	remoteMonitorPod.Name = "hami-device-plugin-node-b"
	remoteMonitorPod.Spec.NodeName = "node-b"
	remoteMonitorPod.Status.PodIP = "10.0.0.3"

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
		WithObjects(endpointPod, monitorPod, remoteMonitorPod).
		Build()
	provider := KubernetesProvider{
		Client:   ctrClient,
		NodeName: "node-a",
		HTTPClient: roundTripClient(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, "http://10.0.0.2:9394/metrics", req.URL.String())

			return textResponse(`
hami_vgpu_memory_limit_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 8589934592
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 4294967296
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 0.75
`), nil
		}),
	}

	usages, err := provider.Usages(context.Background())
	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "GPU-abc", usages[0].GPUUUID)

	allocations, err := provider.Allocations(context.Background(), nil)
	require.NoError(t, err)
	require.Len(t, allocations, 1)
	assert.Equal(t, "team-a", allocations[0].Workspace)
	assert.Equal(t, "chat", allocations[0].Endpoint)
	assert.Equal(t, "chat-abc", allocations[0].InstanceID)
	assert.Equal(t, "default/chat-abc", allocations[0].RuntimeID)
	require.Len(t, allocations[0].Devices, 1)
	assert.Equal(t, "GPU-abc", allocations[0].Devices[0].UUID)
	assert.Equal(t, int64(8192), allocations[0].Devices[0].MemoryMiB)
	assert.Equal(t, int64(50), allocations[0].Devices[0].CoreUnits)
	assert.Equal(t, "node-a", allocations[0].Devices[0].NodeID)
}

func TestKubernetesProviderReturnsNilWhenHAMiMonitorIsMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
		Build()
	provider := KubernetesProvider{Client: ctrClient, NodeName: "node-a"}

	usages, err := provider.Usages(context.Background())
	require.NoError(t, err)
	assert.Nil(t, usages)
}

func hamiPodNodeNameIndex(object client.Object) []string {
	pod, ok := object.(*corev1.Pod)
	if !ok || pod.Spec.NodeName == "" {
		return nil
	}

	return []string{pod.Spec.NodeName}
}

type roundTripperFunc func(req *http.Request) (*http.Response, error)

func (f roundTripperFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return f(req)
}

func roundTripClient(fn roundTripperFunc) *http.Client {
	return &http.Client{Transport: fn}
}

func textResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
