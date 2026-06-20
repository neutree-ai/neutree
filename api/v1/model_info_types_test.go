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
