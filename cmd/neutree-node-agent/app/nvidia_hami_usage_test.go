package app

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNvidiaHAMiEndpointReplicaUsagesFromMetrics(t *testing.T) {
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

	usages := nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, pods)

	require.Len(t, usages, 1)
	assert.Equal(t, "team-a", usages[0].Workspace)
	assert.Equal(t, "k8s-a", usages[0].Cluster)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "chat-abc", usages[0].InstanceID)
	assert.Equal(t, "node-a", usages[0].NodeID)
	assert.Equal(t, "GPU-abc", usages[0].AcceleratorUUID)
	assert.Equal(t, "0", usages[0].VDeviceIndex)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 4294967296.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}

func TestNvidiaHAMiEndpointReplicaUsagesAggregatesUsageAndNormalizesPercentages(t *testing.T) {
	raw := `
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 100
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 200
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 75
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0"} 0.5
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine"} 300
`
	pods := map[podKey]podIdentity{
		{namespace: "default", name: "chat-abc"}: {endpoint: "chat"},
	}

	usages := nvidiaHAMiEndpointReplicaUsagesFromMetrics(raw, pods)

	require.Len(t, usages, 1)
	require.NotNil(t, usages[0].MemoryUsedBytes)
	assert.Equal(t, 300.0, *usages[0].MemoryUsedBytes)
	require.NotNil(t, usages[0].UtilizationRatio)
	assert.Equal(t, 0.75, *usages[0].UtilizationRatio)
}

func TestNvidiaHAMiKubernetesUsageProviderScrapesLocalMonitor(t *testing.T) {
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
	provider := nvidiaHAMiKubernetesUsageProvider{
		Client:   ctrClient,
		NodeName: "node-a",
		HTTPClient: roundTripClient(func(req *http.Request) (*http.Response, error) {
			assert.Equal(t, "http://10.0.0.2:9394/metrics", req.URL.String())
			return textResponse(`
hami_vgpu_memory_used_bytes{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 4294967296
hami_container_device_utilization_ratio{namespace="default",pod="chat-abc",container="engine",device_uuid="GPU-abc",vdevice_index="0",node="node-a"} 0.75
`), nil
		}),
	}

	usages, err := provider.Usages(context.Background())

	require.NoError(t, err)
	require.Len(t, usages, 1)
	assert.Equal(t, "chat", usages[0].Endpoint)
	assert.Equal(t, "GPU-abc", usages[0].AcceleratorUUID)
}

func TestNvidiaHAMiKubernetesUsageProviderReturnsNilWhenMonitorIsMissing(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
		Build()
	provider := nvidiaHAMiKubernetesUsageProvider{Client: ctrClient, NodeName: "node-a"}

	usages, err := provider.Usages(context.Background())

	require.NoError(t, err)
	assert.Nil(t, usages)
}

func TestNvidiaHAMiKubernetesUsageProviderReturnsNilWhenEndpointHasNoLocalMonitor(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "chat-abc",
			Labels:    map[string]string{"app": "inference", "endpoint": "chat"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	remoteMonitor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "hami-device-plugin-node-b",
			Labels:    map[string]string{"app.kubernetes.io/component": hamiDevicePluginComponent},
		},
		Spec:   corev1.PodSpec{NodeName: "node-b"},
		Status: corev1.PodStatus{PodIP: "10.0.0.3"},
	}
	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
		WithObjects(endpointPod, remoteMonitor).
		Build()

	usages, err := (nvidiaHAMiKubernetesUsageProvider{Client: ctrClient, NodeName: "node-a"}).Usages(context.Background())

	require.NoError(t, err)
	assert.Nil(t, usages)
}

func TestNvidiaHAMiKubernetesUsageProviderPropagatesMonitorErrors(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	endpointPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "default",
			Name:      "chat-abc",
			Labels:    map[string]string{"app": "inference", "endpoint": "chat"},
		},
		Spec: corev1.PodSpec{NodeName: "node-a"},
	}
	monitorPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "hami-device-plugin-node-a",
			Labels:    map[string]string{"app.kubernetes.io/component": hamiDevicePluginComponent},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{PodIP: "10.0.0.2"},
	}
	ctrClient := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
		WithObjects(endpointPod, monitorPod).
		Build()
	provider := nvidiaHAMiKubernetesUsageProvider{
		Client:   ctrClient,
		NodeName: "node-a",
		HTTPClient: roundTripClient(func(*http.Request) (*http.Response, error) {
			return textResponseWithStatus(http.StatusServiceUnavailable, "unavailable"), nil
		}),
	}

	usages, err := provider.Usages(context.Background())

	require.Error(t, err)
	assert.Nil(t, usages)
}

func TestNvidiaHAMiKubernetesUsageProviderFiltersLocalPodsAndUsesDefaultClient(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	pods := []client.Object{
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "remote", Labels: map[string]string{"app": "inference", "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-b"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "terminal", Labels: map[string]string{"app": "inference", "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}, Status: corev1.PodStatus{Phase: corev1.PodFailed}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "missing-endpoint", Labels: map[string]string{"app": "inference"}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "chat-b", Labels: map[string]string{"app": "inference", "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
		&corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: "default", Name: "chat-a", Labels: map[string]string{"app": "inference", "endpoint": "chat"}}, Spec: corev1.PodSpec{NodeName: "node-a"}},
	}
	provider := nvidiaHAMiKubernetesUsageProvider{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithObjects(pods...).
			WithIndex(&corev1.Pod{}, "spec.nodeName", hamiPodNodeNameIndex).
			Build(),
		NodeName: "node-a",
	}

	result, err := provider.localEndpointPods(context.Background())

	require.NoError(t, err)
	require.Len(t, result, 2)
	assert.Equal(t, []string{"chat-a", "chat-b"}, []string{result[0].Name, result[1].Name})
	assert.NotNil(t, provider.httpClient())
	monitor, ok, err := provider.localMonitorPod(context.Background())
	require.NoError(t, err)
	assert.False(t, ok)
	assert.Empty(t, monitor.Name)

	metrics, err := provider.scrapeMonitor(context.Background(), "")
	require.NoError(t, err)
	assert.Empty(t, metrics)
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
	return textResponseWithStatus(http.StatusOK, body)
}

func textResponseWithStatus(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
	}
}
