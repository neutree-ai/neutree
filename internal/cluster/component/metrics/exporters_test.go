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

func TestSelectClusterAcceleratorExporterRejectsMultipleMatches(t *testing.T) {
	component := &MetricsComponent{
		ctrlClient: fake.NewClientBuilder().WithObjects(&corev1.Node{
			ObjectMeta: metav1.ObjectMeta{
				Name:   "gpu-node",
				Labels: map[string]string{"accelerator.example.com/gpu": "true"},
			},
		}).Build(),
	}

	exporters, err := component.selectClusterAcceleratorExporter(context.Background(), []metricsAcceleratorExporter{
		{Name: "first", NodeSelector: map[string]string{"accelerator.example.com/gpu": "true"}},
		{Name: "second", NodeSelector: map[string]string{"accelerator.example.com/gpu": "true"}},
	})

	assert.Nil(t, exporters)
	require.ErrorContains(t, err, "currently supports only one")
}

func TestBuildExporterConfigVolumesProjectsUnvalidatedProfileEntries(t *testing.T) {
	configFileData, mounts, volumes, checksum := buildExporterConfigVolumes("test-exporter", []v1.AcceleratorExporterConfigFile{
		{Path: "", Content: "empty-path"},
		{Path: "relative/config.yaml", Content: "relative-path"},
	})

	assert.Equal(t, "empty-path", configFileData["config"])
	assert.Equal(t, "relative-path", configFileData["config.yaml"])
	assert.Len(t, mounts, 2)
	assert.Len(t, volumes, 2)
	assert.NotEmpty(t, checksum)
}
