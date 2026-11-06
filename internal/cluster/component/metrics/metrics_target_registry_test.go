package metrics

import (
	"context"
	"strings"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/metricsconfig"
	"gotest.tools/v3/assert"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestMetricsComponentIncludesOperatorRegistryTargetsInVMAgentConfig(t *testing.T) {
	registry := metricsconfig.NewRegistry()
	registry.Replace("operator/nvidia", []metricsconfig.Target{{
		AcceleratorType: "nvidia_gpu",
		JobName:         "operator-nvidia-dcgm",
		Kind:            metricsconfig.TargetKindAcceleratorExporter,
		Namespace:       "neutree-system",
		Selector: map[string]string{
			"neutree.ai/metrics-target": "accelerator-exporter",
			"owner":                     "nvidia",
		},
		Port:        19400,
		MetricsPath: "/metrics",
	}})

	component := &MetricsComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec:     &v1.ClusterSpec{Version: "v1.1.2"},
		},
		namespace:             "neutree-system",
		metricsRemoteWriteURL: "https://metrics.example.com/write",
		ctrlClient:            fake.NewClientBuilder().Build(),
		metricsTargets:        registry,
	}

	objects, err := component.GetMetricsResources(context.Background())
	assert.NilError(t, err)

	configMap := findMetricsConfigMap(t, objects, "vmagent-config")
	config := configMap.Data["prometheus.yml"]
	assert.Assert(t, strings.Contains(config, "job_name: 'operator-nvidia-dcgm'"))
	assert.Assert(t, strings.Contains(config, "label: neutree.ai/metrics-target=accelerator-exporter,owner=nvidia"))
}
