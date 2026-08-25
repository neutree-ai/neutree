package neutreemetrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	metricsnormalizer "github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

func TestStaticScrapeTargetProviderRequiresProfileTargetForAcceleratorExporter(t *testing.T) {
	provider := StaticScrapeTargetProvider{}

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://127.0.0.1:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Empty(t, acceleratorTargets)
}

func TestStaticScrapeTargetProviderUsesExplicitAcceleratorTarget(t *testing.T) {
	provider := StaticScrapeTargetProvider{
		AcceleratorType:                "ascend_npu",
		AcceleratorExporterPort:        8082,
		AcceleratorExporterMetricsPath: "npu-metrics",
	}

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://127.0.0.1:8082/npu-metrics",
	}}, targets)
}

func TestStaticScrapeTargetProviderPassesThroughExplicitZeroPort(t *testing.T) {
	provider := StaticScrapeTargetProvider{
		AcceleratorType:                "ascend_npu",
		AcceleratorExporterMetricsPath: "npu-metrics",
	}

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://127.0.0.1:0/npu-metrics",
	}}, targets)
}

func TestStaticScrapeTargetProviderSkipsAdapterWithoutProfileTarget(t *testing.T) {
	provider := StaticScrapeTargetProvider{
		AcceleratorType: "nvidia_gpu",
	}

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://127.0.0.1:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Empty(t, acceleratorTargets)
}

func TestKubernetesScrapeTargetProviderDiscoversManagedPodsOnLocalNode(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "node-exporter-a", "node-a", "10.244.0.10", map[string]string{"app": "neutree-node-exporter"}),
		pod("metrics", "node-exporter-b", "node-b", "10.244.0.11", map[string]string{"app": "neutree-node-exporter"}),
		pod("metrics", "custom-exporter-a", "node-a", "10.244.0.12", map[string]string{
			"app":                          "custom-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
		}),
	)
	provider.NodeName = "node-a"

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://10.244.0.10:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Empty(t, acceleratorTargets)
}

func TestKubernetesScrapeTargetProviderUsesExplicitAdapterTarget(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "npu-exporter-a", "node-a", "10.244.0.30", map[string]string{
			"app":                          "ascend-npu-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
			AcceleratorExporterTypeLabel:   "ascend_npu",
		}),
		pod("metrics", "other-exporter-a", "node-a", "10.244.0.32", map[string]string{
			"app":                          "other-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
			AcceleratorExporterTypeLabel:   "other_accelerator",
		}),
		pod("metrics", "legacy-exporter-a", "node-a", "10.244.0.31", map[string]string{
			"app": "legacy-dcgm-exporter",
		}),
	)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "ascend_npu"
	provider.AcceleratorExporterPort = 8082
	provider.AcceleratorExporterMetricsPath = "npu-metrics"

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://10.244.0.30:8082/npu-metrics",
	}}, targets)
}

func TestKubernetesScrapeTargetProviderPassesThroughExplicitZeroPort(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "npu-exporter-a", "node-a", "10.244.0.30", map[string]string{
			"app":                          "ascend-npu-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
			AcceleratorExporterTypeLabel:   "ascend_npu",
		}),
	)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "ascend_npu"
	provider.AcceleratorExporterMetricsPath = "npu-metrics"

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://10.244.0.30:0/npu-metrics",
	}}, targets)
}

func TestKubernetesScrapeTargetProviderSkipsLegacyExternalAcceleratorPods(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "node-exporter-a", "node-a", "10.244.0.20", map[string]string{"app": "neutree-node-exporter"}),
		pod("monitoring", "external-node-exporter-a", "node-a", "10.244.0.21", map[string]string{"app.kubernetes.io/name": "node-exporter"}),
		pod("gpu", "dcgm-a", "node-a", "10.244.0.22", map[string]string{"app": "nvidia-dcgm-exporter"}),
		pod("gpu", "dcgm-empty-ip", "node-a", "", map[string]string{"app": "nvidia-dcgm-exporter"}),
	)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "nvidia_gpu"

	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{
		{TargetType: metricsnormalizer.TargetNodeExporter, URL: "http://10.244.0.20:19100/metrics"},
	}, nodeTargets)

	acceleratorTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)
	require.NoError(t, err)
	assert.Empty(t, acceleratorTargets)
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
		Client: fake.NewClientBuilder().
			WithScheme(scheme).
			WithRuntimeObjects(objects...).
			WithIndex(&corev1.Pod{}, "spec.nodeName", func(obj client.Object) []string {
				pod, ok := obj.(*corev1.Pod)
				if !ok || pod.Spec.NodeName == "" {
					return nil
				}

				return []string{pod.Spec.NodeName}
			}).
			Build(),
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
