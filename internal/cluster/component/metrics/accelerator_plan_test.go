package metrics

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestSelectClusterAcceleratorPlanRejectsMultipleMatches(t *testing.T) {
	component := &MetricsComponent{
		ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "node-a",
				Labels: map[string]string{"vendor.example/gpu": "true", "vendor.example/accelerator": "true"},
			},
		}).Build(),
	}

	_, err := component.selectClusterAcceleratorPlan(context.Background(), []metricsAcceleratorPlan{
		{
			AcceleratorType: "nvidia_gpu",
			Exporter: &metricsAcceleratorExporter{
				NodeSelector: map[string]string{"vendor.example/gpu": "true"},
			},
		},
		{
			AcceleratorType: "vendor_accelerator",
			Exporter: &metricsAcceleratorExporter{
				NodeSelector: map[string]string{"vendor.example/accelerator": "true"},
			},
		},
	})

	require.Error(t, err)
	assert.ErrorContains(t, err, "currently supports only one matching accelerator exporter")
}

func TestSelectClusterAcceleratorPlanExternalTargetIgnoresManagedNodeSelector(t *testing.T) {
	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Spec: &v1.ClusterSpec{
				Config: &v1.ClusterConfig{Metrics: &v1.ClusterMetricsConfig{
					AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
						Mode: v1.ClusterAcceleratorExporterModeExternal,
					},
				}},
			},
		},
		ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{Name: "node-a"},
		}).Build(),
	}

	selected, err := component.selectClusterAcceleratorPlan(context.Background(), []metricsAcceleratorPlan{
		{
			AcceleratorType: "vendor_accelerator",
			Exporter: &metricsAcceleratorExporter{
				NodeSelector: map[string]string{"vendor.example/accelerator": "true"},
			},
			ExternalMetricsTarget: &v1.MetricsTargetProfile{
				Namespace:   "monitoring",
				PodSelector: map[string]string{"app": "external-exporter"},
				Port:        9400,
			},
		},
	})

	require.NoError(t, err)
	require.Len(t, selected, 1)
	assert.Equal(t, "vendor_accelerator", selected[0].AcceleratorType)
}

func TestSelectClusterAcceleratorPlanSkipsWhenNoMatch(t *testing.T) {
	component := &MetricsComponent{
		ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "cpu-node",
				Labels: map[string]string{"kubernetes.io/os": "linux"},
			},
		}).Build(),
	}

	matches, err := component.selectClusterAcceleratorPlan(context.Background(), []metricsAcceleratorPlan{{
		AcceleratorType: "nvidia_gpu",
		Exporter: &metricsAcceleratorExporter{
			NodeSelector: map[string]string{"vendor.example/gpu": "true"},
		},
	}})

	require.NoError(t, err)
	assert.Nil(t, matches)
}
