package metrics

import (
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

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
