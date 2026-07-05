package neutreemetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

func TestStaticScrapeTargetProviderUsesManagedPorts(t *testing.T) {
	provider := StaticScrapeTargetProvider{MetricsMode: MetricsModeManaged}

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://127.0.0.1:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "http://127.0.0.1:19400/metrics"},
	}, acceleratorTargets)
}

func TestStaticScrapeTargetProviderUsesExternalPortsWithHTTPSFallback(t *testing.T) {
	provider := StaticScrapeTargetProvider{MetricsMode: MetricsModeExternal}

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://127.0.0.1:9100/metrics"},
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "https://127.0.0.1:9100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "http://127.0.0.1:9400/metrics"},
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "https://127.0.0.1:9400/metrics"},
	}, acceleratorTargets)
}

func TestKubernetesScrapeTargetProviderDiscoversManagedPodsOnLocalNode(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "node-exporter-a", "node-a", "10.244.0.10", map[string]string{"app": "neutree-node-exporter"}),
		pod("metrics", "node-exporter-b", "node-b", "10.244.0.11", map[string]string{"app": "neutree-node-exporter"}),
		pod("metrics", "custom-exporter-a", "node-a", "10.244.0.12", map[string]string{
			"app":                           "custom-exporter",
			ManagedAcceleratorExporterLabel: ManagedAcceleratorExporterValue,
		}),
	)
	provider.MetricsMode = MetricsModeManaged
	provider.NodeName = "node-a"

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://10.244.0.10:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "http://10.244.0.12:19400/metrics"},
	}, acceleratorTargets)
}

func TestKubernetesScrapeTargetProviderDiscoversExternalPodsOnLocalNode(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("monitoring", "node-exporter-a", "node-a", "10.244.0.20", map[string]string{"app.kubernetes.io/name": "node-exporter"}),
		pod("monitoring", "node-exporter-b", "node-b", "10.244.0.21", map[string]string{"app.kubernetes.io/name": "node-exporter"}),
		pod("gpu", "dcgm-a", "node-a", "10.244.0.22", map[string]string{"app": "nvidia-dcgm-exporter"}),
		pod("gpu", "dcgm-empty-ip", "node-a", "", map[string]string{"app": "nvidia-dcgm-exporter"}),
	)
	provider.MetricsMode = MetricsModeExternal
	provider.NodeName = "node-a"

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://10.244.0.20:9100/metrics"},
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "https://10.244.0.20:9100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "http://10.244.0.22:9400/metrics"},
		{TargetType: metricsnormalizer.TargetAcceleratorExporter, URL: "https://10.244.0.22:9400/metrics"},
	}, acceleratorTargets)
}

func newKubernetesTargetProvider(t *testing.T, pods ...*corev1.Pod) KubernetesScrapeTargetProvider {
	t.Helper()

	scheme := runtime.NewScheme()
	require.NoError(t, corev1.AddToScheme(scheme))
	objects := make([]runtime.Object, 0, len(pods))
	for _, pod := range pods {
		objects = append(objects, pod)
	}

	return KubernetesScrapeTargetProvider{
		Client: fake.NewClientBuilder().WithScheme(scheme).WithRuntimeObjects(objects...).Build(),
	}
}

func pod(namespace, name, node, ip string, labels map[string]string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Namespace: namespace,
			Name:      name,
			Labels:    labels,
		},
		Spec: corev1.PodSpec{NodeName: node},
		Status: corev1.PodStatus{
			PodIP: ip,
		},
	}
}
