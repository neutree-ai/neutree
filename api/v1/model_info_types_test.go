package v1

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// ModelInfo is display-only metadata and must round-trip through JSON both as a
// standalone struct and when nested inside a recipe variant's model — that is
// the path the catalog card / show page reads it from.
func TestModelInfo_JSONRoundTrip(t *testing.T) {
	info := &ModelInfo{
		ParameterCount: "72.7B",
		Quantization:   "fp8",
		ContextLength:  "32K",
		Architecture:   "dense",
	}

	buf, err := json.Marshal(info)
	require.NoError(t, err)

	var got ModelInfo
	require.NoError(t, json.Unmarshal(buf, &got))
	assert.Equal(t, *info, got)
}

func TestModelSpec_InfoOmittedWhenNil(t *testing.T) {
	buf, err := json.Marshal(ModelSpec{Name: "qwen3", Registry: "huggingface"})
	require.NoError(t, err)
	// Optional + forward-compatible: a spec without model info must not emit an
	// "info" key, so legacy catalogs/endpoints serialize exactly as before.
	assert.NotContains(t, string(buf), "info")
}

func TestRecipeVariant_CarriesModelInfo(t *testing.T) {
	v := RecipeVariant{
		Model: &ModelSpec{
			Name: "Qwen/Qwen3-FP8",
			Info: &ModelInfo{ParameterCount: "27B", Quantization: "fp8", ContextLength: "128K", Architecture: "moe"},
		},
	}

	buf, err := json.Marshal(v)
	require.NoError(t, err)

	var got RecipeVariant
	require.NoError(t, json.Unmarshal(buf, &got))
	require.NotNil(t, got.Model)
	require.NotNil(t, got.Model.Info)
	assert.Equal(t, "27B", got.Model.Info.ParameterCount)
	assert.Equal(t, "moe", got.Model.Info.Architecture)
}

// Every structured field added to ModelInfo is optional, so a spec written
// before they existed has to survive a decode/encode cycle byte for byte. This
// is the compatibility guard for the stored catalog and endpoint specs.
func TestModelSpec_LegacyJSONRoundTripsByteForByte(t *testing.T) {
	const legacy = `{"registry":"y","name":"x"}`

	var spec ModelSpec
	require.NoError(t, json.Unmarshal([]byte(legacy), &spec))

	buf, err := json.Marshal(spec)
	require.NoError(t, err)
	assert.Equal(t, legacy, string(buf))
}

// A ModelInfo holding only the four hand-written display fields must serialize
// exactly as it did before, so the structured fields cannot leak zero values
// into an existing catalog.
func TestModelInfo_DisplayOnlyJSONRoundTripsByteForByte(t *testing.T) {
	const display = `{"parameter_count":"72.7B","quantization":"fp8","context_length":"32K","architecture":"dense"}`

	var info ModelInfo
	require.NoError(t, json.Unmarshal([]byte(display), &info))

	buf, err := json.Marshal(info)
	require.NoError(t, err)
	assert.Equal(t, display, string(buf))
}

func TestModelInfo_StructuredFieldsRoundTrip(t *testing.T) {
	layers, heads, kvHeads, headDim := 64, 40, 8, 128
	maxPos, experts, expertsPerToken, bits := 32768, 128, 8, 4
	isMoE := true

	info := &ModelInfo{
		ParameterCount:        "235093634560",
		ContextLength:         "32768",
		Architecture:          "Qwen3MoeForCausalLM",
		NumHiddenLayers:       &layers,
		NumAttentionHeads:     &heads,
		NumKeyValueHeads:      &kvHeads,
		HeadDim:               &headDim,
		MaxPositionEmbeddings: &maxPos,
		IsMoE:                 &isMoE,
		NumExperts:            &experts,
		NumExpertsPerToken:    &expertsPerToken,
		ParameterDtype:        "bfloat16",
		QuantizationBits:      &bits,
		FieldSources: map[string]string{
			ModelInfoFieldParameterCount: ModelInfoSourceAuto,
			ModelInfoFieldHeadDim:        ModelInfoSourceDerived,
			ModelInfoFieldQuantization:   ModelInfoSourceManual,
		},
		MissingFields: []string{ModelInfoFieldQuantizationBits},
	}

	buf, err := json.Marshal(info)
	require.NoError(t, err)

	var got ModelInfo
	require.NoError(t, json.Unmarshal(buf, &got))
	assert.Equal(t, *info, got)
}

// A zero value must not claim knowledge it does not have: no field keys, and no
// empty provenance containers either.
func TestModelInfo_ZeroValueEmitsNothing(t *testing.T) {
	buf, err := json.Marshal(ModelInfo{})
	require.NoError(t, err)
	assert.Equal(t, "{}", string(buf))
}

func TestModelInfo_FieldProvenanceBookkeeping(t *testing.T) {
	var info ModelInfo

	info.MarkFieldMissing(ModelInfoFieldHeadDim)
	info.MarkFieldMissing(ModelInfoFieldParameterCount)
	info.MarkFieldMissing(ModelInfoFieldHeadDim)
	assert.Equal(t, []string{ModelInfoFieldHeadDim, ModelInfoFieldParameterCount}, info.MissingFields)

	// A later pass that establishes a value has to retract the earlier claim
	// that the field is unknown, otherwise the field is both set and missing.
	info.SetFieldSource(ModelInfoFieldHeadDim, ModelInfoSourceManual)
	info.ClearFieldMissing(ModelInfoFieldHeadDim)
	assert.Equal(t, []string{ModelInfoFieldParameterCount}, info.MissingFields)
	assert.Equal(t, ModelInfoSourceManual, info.FieldSources[ModelInfoFieldHeadDim])

	// Marking a sourced field missing again drops the stale provenance.
	info.MarkFieldMissing(ModelInfoFieldHeadDim)
	assert.NotContains(t, info.FieldSources, ModelInfoFieldHeadDim)

	info.ClearFieldMissing(ModelInfoFieldHeadDim)
	info.ClearFieldMissing(ModelInfoFieldParameterCount)
	assert.Nil(t, info.MissingFields)
}
