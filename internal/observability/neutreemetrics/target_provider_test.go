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

func TestStaticScrapeTargetProviderUsesProfileAcceleratorTarget(t *testing.T) {
	provider := StaticScrapeTargetProvider{
		AcceleratorType:                "test_accelerator",
		AcceleratorExporterPort:        8082,
		AcceleratorExporterMetricsPath: "accelerator-metrics",
	}

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://127.0.0.1:8082/accelerator-metrics",
	}}, targets)
}

func TestStaticScrapeTargetProviderPassesThroughProfileZeroPort(t *testing.T) {
	provider := StaticScrapeTargetProvider{
		AcceleratorType:                "test_accelerator",
		AcceleratorExporterMetricsPath: "accelerator-metrics",
	}

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://127.0.0.1:0/accelerator-metrics",
	}}, targets)
}

func TestStaticScrapeTargetProviderUsesDefaultProfilePath(t *testing.T) {
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
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://127.0.0.1:0/metrics",
	}}, acceleratorTargets)
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

func TestKubernetesScrapeTargetProviderUsesProfileAdapterTarget(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "accelerator-exporter-a", "node-a", "10.244.0.30", map[string]string{
			"app":                          "test-accelerator-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
			AcceleratorExporterTypeLabel:   "test_accelerator",
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
	provider.AcceleratorType = "test_accelerator"
	provider.AcceleratorExporterPort = 8082
	provider.AcceleratorExporterMetricsPath = "accelerator-metrics"

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://10.244.0.30:8082/accelerator-metrics",
	}}, targets)
}

func TestKubernetesScrapeTargetProviderPassesThroughProfileZeroPort(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "accelerator-exporter-a", "node-a", "10.244.0.30", map[string]string{
			"app":                          "test-accelerator-exporter",
			AcceleratorExporterTargetLabel: AcceleratorExporterTargetValue,
			AcceleratorExporterTypeLabel:   "test_accelerator",
		}),
	)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "test_accelerator"
	provider.AcceleratorExporterMetricsPath = "accelerator-metrics"

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://10.244.0.30:0/accelerator-metrics",
	}}, targets)
}

func TestKubernetesScrapeTargetProviderUsesExternalSelectorAndNamespace(t *testing.T) {
	provider := newKubernetesTargetProvider(t,
		pod("metrics", "node-exporter-a", "node-a", "10.244.0.09", map[string]string{"app": "neutree-node-exporter"}),
		pod("monitoring", "external-a", "node-a", "10.244.0.40", map[string]string{
			"app":  "external-exporter",
			"role": "accelerator",
		}),
		pod("metrics", "same-label-wrong-namespace", "node-a", "10.244.0.41", map[string]string{
			"app":  "external-exporter",
			"role": "accelerator",
		}),
		pod("monitoring", "wrong-selector", "node-a", "10.244.0.42", map[string]string{
			"app": "external-exporter",
		}),
	)
	provider.NodeName = "node-a"
	provider.AcceleratorType = "vendor_accelerator"
	provider.AcceleratorExporterPort = 9400
	provider.AcceleratorExporterMetricsPath = "/custom/metrics"
	provider.AcceleratorExporterNamespace = "monitoring"
	provider.AcceleratorExporterPodSelector = map[string]string{
		"app":  "external-exporter",
		"role": "accelerator",
	}

	targets, err := provider.Targets(context.Background(), metricsnormalizer.TargetAcceleratorExporter)

	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetAcceleratorExporter,
		URL:        "http://10.244.0.40:9400/custom/metrics",
	}}, targets)

	// The external namespace is scoped to accelerator exporter discovery only;
	// unrelated node-exporter discovery must keep using the cluster metrics namespace.
	nodeTargets, err := provider.Targets(context.Background(), metricsnormalizer.TargetNodeExporter)
	require.NoError(t, err)
	assert.Equal(t, []ScrapeTarget{{
		TargetType: metricsnormalizer.TargetNodeExporter,
		URL:        "http://10.244.0.09:19100/metrics",
	}}, nodeTargets)
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
