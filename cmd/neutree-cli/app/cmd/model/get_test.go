package model

import (
	"bytes"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// detailLines renders a version and returns the output with the tabwriter's
// alignment collapsed, so assertions read like the labels and values they check.
func detailLines(t *testing.T, modelName string, modelVersion *v1.ModelVersion) []string {
	t.Helper()

	var out bytes.Buffer
	require.NoError(t, renderModelDetail(&out, modelName, modelVersion))

	lines := strings.Split(strings.TrimRight(out.String(), "\n"), "\n")
	for i, line := range lines {
		lines[i] = strings.Join(strings.Fields(line), " ")
	}

	return lines
}

func TestGetLabelsAreAPIFieldNames(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{
		Name:         "v1",
		Alias:        "pet",
		Size:         "64 B",
		CreationTime: "2026-06-25T00:00:00Z",
	})

	require.Equal(t, []string{
		"model: demo",
		"version: v1",
		"alias: pet",
		"size: 64 B",
		"creation_time: 2026-06-25T00:00:00Z",
	}, lines)
}

// An unset alias and an unestablished field are different facts and must not
// render alike: "nobody named this version" is not "the registry could not tell".
func TestGetDistinguishesUnsetAliasFromUnknownField(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{Name: "v1", CreationTime: "2026-06-25T00:00:00Z"})

	require.Contains(t, lines, "alias: "+unsetValue)
	require.Contains(t, lines, "size: "+unknownValue)
	require.NotContains(t, lines, "alias: "+unknownValue)
}

func TestGetRendersModelInfoWithProvenance(t *testing.T) {
	layers := 28
	headDim := 128

	lines := detailLines(t, "demo", &v1.ModelVersion{
		Name: "v1",
		Info: &v1.ModelInfo{
			ParameterCount:  "7615616512",
			Architecture:    "Qwen2ForCausalLM",
			NumHiddenLayers: &layers,
			HeadDim:         &headDim,
			ContextLength:   "32K",
			FieldSources: map[string]string{
				v1.ModelInfoFieldParameterCount:  v1.ModelInfoSourceAuto,
				v1.ModelInfoFieldArchitecture:    v1.ModelInfoSourceAuto,
				v1.ModelInfoFieldNumHiddenLayers: v1.ModelInfoSourceAuto,
				v1.ModelInfoFieldHeadDim:         v1.ModelInfoSourceDerived,
				v1.ModelInfoFieldContextLength:   v1.ModelInfoSourceManual,
			},
			MissingFields: []string{v1.ModelInfoFieldQuantization},
		},
	})

	require.Contains(t, lines, "info:")
	require.Contains(t, lines, "parameter_count: 7615616512 (auto)")
	require.Contains(t, lines, "head_dim: 128 (derived)")
	require.Contains(t, lines, "context_length: 32K (manual)")

	// Named in missing_fields: the registry looked and came up empty.
	require.Contains(t, lines, "quantization: "+unknownValue)

	// Absent without being named there: the field does not apply to this model,
	// so listing it as unknown would invent a gap.
	for _, line := range lines {
		require.NotEqual(t, "num_experts: "+unknownValue, line)
		require.NotEqual(t, "is_moe: "+unknownValue, line)
	}
}

// A value carried over from a catalog written before provenance was tracked has
// no source. It is shown plainly; annotating it "unknown" would read as a
// missing value rather than an unrecorded origin.
func TestGetLeavesUnrecordedProvenanceUnannotated(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{
		Name: "v1",
		Info: &v1.ModelInfo{ParameterCount: "72.7B"},
	})

	require.Contains(t, lines, "parameter_count: 72.7B")
}

// A server ahead of this build can report a field it has no row for. Naming it
// is the point of missing_fields, so it is printed rather than dropped.
func TestGetReportsUnrecognisedMissingFields(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{
		Name: "v1",
		Info: &v1.ModelInfo{MissingFields: []string{"vocab_size", v1.ModelInfoFieldArchitecture}},
	})

	require.Contains(t, lines, "architecture: "+unknownValue)
	require.Contains(t, lines, "vocab_size: "+unknownValue)
}

// A listing leaves Info nil rather than opening every checkpoint; that is not a
// model whose every field is unknown, so no info block is written at all.
func TestGetOmitsInfoBlockWhenAbsent(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{Name: "v1"})

	require.NotContains(t, lines, "info:")
}

func TestGetSortsLabelsForAStableBlock(t *testing.T) {
	lines := detailLines(t, "demo", &v1.ModelVersion{
		Name:   "v1",
		Labels: map[string]string{"zone": "b", "app": "chat", "tier": "gold"},
	})

	start := len(lines) - 3
	require.Equal(t, []string{"app: chat", "tier: gold", "zone: b"}, lines[start:])
}

func TestFormatPointerValues(t *testing.T) {
	value := 7
	yes := true

	require.Equal(t, "7", formatInt(&value))
	require.Equal(t, "", formatInt(nil))
	require.Equal(t, "true", formatBool(&yes))
	require.Equal(t, "", formatBool(nil))

	// A zero is a value, not an absence — which is why these fields are pointers.
	zero := 0
	no := false
	require.Equal(t, "0", formatInt(&zero))
	require.Equal(t, "false", formatBool(&no))
}
