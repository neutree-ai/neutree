package adapter

import (
	"context"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/model"
	"github.com/neutree-ai/neutree/internal/observability/neutreemetrics/normalizer"
)

// fakeAccelerator is a minimal Accelerator implementation used to exercise the
// registry without depending on a real vendor adapter.
type fakeAccelerator struct {
	typ string
}

func (f *fakeAccelerator) Type() string { return f.typ }

func (f *fakeAccelerator) BuildMetrics(
	_ context.Context,
	_ AcceleratorEvidence,
) (AcceleratorMetricResult, error) {
	return AcceleratorMetricResult{}, nil
}

func TestRegistryRegistersAndReturnsLocalAccelerators(t *testing.T) {
	Register(&fakeAccelerator{typ: "fake-accel"})

	adapters := GetLocalAccelerators()

	accel, ok := adapters["fake-accel"]
	require.True(t, ok)
	assert.Equal(t, "fake-accel", accel.Type())
}

func TestRegistryGetLocalAcceleratorsReturnsStableMap(t *testing.T) {
	Register(&fakeAccelerator{typ: "fake-stable"})

	first := GetLocalAccelerators()
	second := GetLocalAccelerators()

	firstAccel, firstOK := first["fake-stable"]
	secondAccel, secondOK := second["fake-stable"]
	assert.True(t, firstOK)
	assert.True(t, secondOK)
	assert.Equal(t, firstAccel, secondAccel)
}

func TestRegistryLookupMissingType(t *testing.T) {
	Register(&fakeAccelerator{typ: "fake-present"})

	adapters := GetLocalAccelerators()

	_, ok := adapters["does-not-exist"]
	assert.False(t, ok)
}

func TestNvidiaAcceleratorTypeIsNVIDIAGPU(t *testing.T) {
	accel := &nvidiaAccelerator{}

	assert.Equal(t, v1.AcceleratorTypeNVIDIAGPU.String(), accel.Type())
}

func TestNvidiaAcceleratorBuildMetricsEmptyWhenExporterDown(t *testing.T) {
	accel := &nvidiaAccelerator{}

	result, err := accel.BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterUp:      false,
	})

	require.NoError(t, err)
	assert.Empty(t, result.Samples)
	assert.Empty(t, result.DeviceSnapshots)
}

func TestNvidiaAcceleratorBuildMetricsProducesAcceleratorSamples(t *testing.T) {
	accel := &nvidiaAccelerator{}
	raw := `DCGM_FI_DEV_GPU_UTIL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 87
DCGM_FI_DEV_FB_USED{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 43008
DCGM_FI_DEV_FB_TOTAL{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 81920
DCGM_FI_DEV_GPU_TEMP{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 72
DCGM_FI_PROF_PCIE_TX_BYTES{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 1024
DCGM_FI_PROF_PCIE_RX_BYTES{gpu="0",UUID="GPU-abc",device="nvidia0",modelName="A100"} 2048
`
	result, err := accel.BuildMetrics(context.Background(), AcceleratorEvidence{
		AcceleratorType: v1.AcceleratorTypeNVIDIAGPU.String(),
		ExporterText:    raw,
		ExporterUp:      true,
		Labels:          testLabels(),
	})

	require.NoError(t, err)
	output := formatSamples(result.Samples)

	assert.Contains(t, output, `neutree_accelerator_utilization_ratio{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 0.87`)
	assert.Contains(t, output, `neutree_accelerator_memory_used_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 45097156608`)
	assert.Contains(t, output, `neutree_accelerator_memory_total_bytes{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 85899345920`)
	assert.Contains(t, output, `neutree_accelerator_temperature_celsius{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 72`)
	assert.Contains(t, output, `neutree_accelerator_pcie_tx_bytes_total{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 1024`)
	assert.Contains(t, output, `neutree_accelerator_pcie_rx_bytes_total{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 2048`)
	assert.Contains(t, output, `neutree_node_accelerator_total{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_info{accelerator_index="0",accelerator_type="nvidia_gpu",accelerator_uuid="GPU-abc",cluster_type="ray",node="head-0",product="A100"} 1`)
	assert.Contains(t, output, `neutree_node_accelerator_allocated{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 0`)
	assert.Contains(t, output, `neutree_node_accelerator_free{accelerator_type="nvidia_gpu",cluster_type="ray",node="head-0",product="A100"} 1`)
}

func testLabels() model.CanonicalLabels {
	return model.CanonicalLabels{
		Workspace:         "default",
		StaticNodeCluster: "static-a",
		ClusterType:       "ray",
		Node:              "head-0",
		NodeIP:            "10.0.0.10",
		NodeRole:          "head",
	}
}

func formatSamples(samples []normalizer.Sample) string {
	var builder strings.Builder
	for _, sample := range samples {
		builder.WriteString(formatSample(sample))
		builder.WriteByte('\n')
	}

	return builder.String()
}

func formatSample(s normalizer.Sample) string {
	return fmt.Sprintf("%s%s %s", s.Name, formatLabels(s.Labels), formatFloat(s.Value))
}

func formatLabels(labels map[string]string) string {
	if len(labels) == 0 {
		return ""
	}

	keys := make([]string, 0, len(labels))
	for key := range labels {
		keys = append(keys, key)
	}

	sort.Strings(keys)

	parts := make([]string, 0, len(labels))
	for _, key := range keys {
		parts = append(parts, key+`="`+escapeLabelValue(labels[key])+`"`)
	}

	return "{" + strings.Join(parts, ",") + "}"
}

func escapeLabelValue(value string) string {
	value = strings.ReplaceAll(value, `\`, `\\`)
	value = strings.ReplaceAll(value, "\n", `\n`)

	return strings.ReplaceAll(value, `"`, `\"`)
}

func formatFloat(value float64) string {
	if math.Trunc(value) == value {
		return strconv.FormatInt(int64(value), 10)
	}

	return strconv.FormatFloat(value, 'f', -1, 64)
}
