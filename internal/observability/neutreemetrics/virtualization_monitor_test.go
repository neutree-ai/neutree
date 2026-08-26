package neutreemetrics

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
	metricskubernetes "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/kubernetes"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestKubernetesVirtualizationMonitorCollectorUsesProfileForLocalPod(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	localMonitor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "hami-local",
			Labels:    map[string]string{"app.kubernetes.io/component": "hami-device-plugin"},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{PodIP: "10.0.0.2"},
	}
	remoteMonitor := localMonitor.DeepCopy()
	remoteMonitor.Name = "hami-remote"
	remoteMonitor.Spec.NodeName = "node-b"
	remoteMonitor.Status.PodIP = "10.0.0.3"

	collector := KubernetesVirtualizationMonitorCollector{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
				return []string{object.(*corev1.Pod).Spec.NodeName}
			}).
			WithObjects(localMonitor, remoteMonitor).
			Build(),
		NodeName: "node-a",
		Profile: &v1.VirtualizationMonitorProfile{
			Namespace:   "kube-system",
			PodSelector: map[string]string{"app.kubernetes.io/component": "hami-device-plugin"},
			Port:        9394,
			MetricsPath: "hami-metrics",
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(request *http.Request) (*http.Response, error) {
			assert.Equal(t, "http://10.0.0.2:9394/hami-metrics", request.URL.String())
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("hami_vgpu_memory_used_bytes 1\n")),
				Header:     make(http.Header),
			}, nil
		})},
	}

	text, up, err := collector.Collect(context.Background())

	require.NoError(t, err)
	assert.True(t, up)
	assert.Equal(t, "hami_vgpu_memory_used_bytes 1\n", text)
}

func TestKubernetesVirtualizationMonitorCollectorSkipsProcessingWithoutLocalMonitor(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	collector := KubernetesVirtualizationMonitorCollector{
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
				return []string{object.(*corev1.Pod).Spec.NodeName}
			}).
			Build(),
		NodeName: "node-a",
		Profile: &v1.VirtualizationMonitorProfile{
			PodSelector: map[string]string{"app.kubernetes.io/component": "hami-device-plugin"},
			Port:        9394,
		},
	}

	text, up, err := collector.Collect(context.Background())

	require.NoError(t, err)
	assert.Empty(t, text)
	assert.False(t, up)
}

func TestServerAddsRawVirtualizationMonitorEvidence(t *testing.T) {
	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))

	monitor := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: "kube-system",
			Name:      "hami-local",
			Labels:    map[string]string{"app.kubernetes.io/component": "hami-device-plugin"},
		},
		Spec:   corev1.PodSpec{NodeName: "node-a"},
		Status: corev1.PodStatus{PodIP: "10.0.0.2"},
	}
	client := fake.NewClientBuilder().
		WithScheme(scheme).
		WithIndex(&corev1.Pod{}, "spec.nodeName", func(object client.Object) []string {
			return []string{object.(*corev1.Pod).Spec.NodeName}
		}).
		WithObjects(monitor).
		Build()

	server, err := NewServer(Config{
		KubernetesWriter: &metricskubernetes.AnnotationWriter{Client: client, NodeName: "node-a"},
		VirtualizationMonitor: &v1.VirtualizationMonitorProfile{
			Namespace:   "kube-system",
			PodSelector: map[string]string{"app.kubernetes.io/component": "hami-device-plugin"},
			Port:        9394,
		},
		HTTPClient: &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) {
			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(strings.NewReader("hami_container_device_utilization_ratio 0.5\n")),
				Header:     make(http.Header),
			}, nil
		})},
		KubernetesAcceleratorEvidenceProvider: fakeKubernetesAcceleratorEvidenceProvider{
			evidence: adapter.KubernetesEvidence{AllocationAvailable: true},
		},
	})
	require.NoError(t, err)

	evidence := server.kubernetesAcceleratorEvidence(context.Background(), adapter.CommonEvidence{})

	assert.True(t, evidence.VirtualizationMonitorUp)
	assert.Equal(t, "hami_container_device_utilization_ratio 0.5\n", evidence.VirtualizationMonitorText)
}
