package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestManagedAcceleratorExportersClearsNVIDIASelectorOnlyForRender(t *testing.T) {
	nvidiaSelector := map[string]string{"nvidia.com/gpu.present": "true"}
	customSelector := map[string]string{"accelerator.example.com/custom": "true"}
	plans := []metricsAcceleratorPlan{
		{Exporter: &metricsAcceleratorExporter{
			AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
			NodeSelector:    nvidiaSelector,
		}},
		{Exporter: &metricsAcceleratorExporter{
			AcceleratorType: "custom_accelerator",
			NodeSelector:    customSelector,
		}},
	}

	exporters := managedAcceleratorExporters(plans)
	require.Len(t, exporters, 2)
	assert.Nil(t, exporters[0].NodeSelector)
	assert.Equal(t, nvidiaSelector, plans[0].Exporter.NodeSelector)
	assert.Equal(t, customSelector, exporters[1].NodeSelector)
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
