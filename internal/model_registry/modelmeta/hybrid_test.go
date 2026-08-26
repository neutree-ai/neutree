package modelmeta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// The window is only reported when the same config also establishes that it is
// in force. The two cases that matter are real checkpoints pulling in opposite
// directions: gpt-oss-20b states sliding_window and a layer_types list naming
// the layers it applies to, while Qwen2-7B-Instruct states a larger
// sliding_window next to use_sliding_window: false and caches every token.
func TestParseConfig_SlidingWindowNeedsItsLayersPlaced(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		want        *int
		wantMissing bool
	}{
		{
			name:   "a bare window is not placed on any layer",
			config: `{"num_hidden_layers":28,"sliding_window":131072}`,
		},
		{
			name:   "use_sliding_window false is the checkpoint saying no",
			config: `{"num_hidden_layers":28,"sliding_window":131072,"use_sliding_window":false}`,
		},
		{
			name: "use_sliding_window false outranks a layer_types list",
			config: `{"num_hidden_layers":2,"sliding_window":4096,"use_sliding_window":false,` +
				`"layer_types":["sliding_attention","full_attention"]}`,
		},
		{
			name: "layer_types names the layers the window applies to",
			config: `{"num_hidden_layers":2,"sliding_window":128,` +
				`"layer_types":["sliding_attention","full_attention"]}`,
			want: ptr(128),
		},
		{
			name:   "a compression schedule places the window on every layer",
			config: `{"num_hidden_layers":2,"sliding_window":128,"compress_ratios":[128,4]}`,
			want:   ptr(128),
		},
		{
			name: "sliding layers with no window stated is a real gap",
			config: `{"num_hidden_layers":2,` +
				`"layer_types":["sliding_attention","full_attention"]}`,
			wantMissing: true,
		},
		{
			name:   "full attention throughout needs no window",
			config: `{"num_hidden_layers":2,"layer_types":["full_attention","full_attention"]}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ParseConfig([]byte(tt.config))

			assert.Equal(t, tt.want, info.SlidingWindow)
			assert.Equal(t, tt.wantMissing, slices.Contains(info.MissingFields, v1.ModelInfoFieldSlidingWindow))

			if tt.want != nil {
				assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldSlidingWindow])
			}
		})
	}
}

// The five linear-attention widths are a set: the state size is their product,
// so a config that describes such a layer and omits one of them has a real gap,
// and a config that describes no such layer has none.
func TestParseConfig_LinearAttentionWidthsAreReportedAsASet(t *testing.T) {
	full := `{"num_hidden_layers":4,"layer_types":["linear_attention","linear_attention",` +
		`"linear_attention","full_attention"],"linear_conv_kernel_dim":4,"linear_num_key_heads":16,` +
		`"linear_key_head_dim":128,"linear_num_value_heads":48,"linear_value_head_dim":128,` +
		`"mamba_ssm_dtype":"float32"}`

	info := ParseConfig([]byte(full))

	assert.Equal(t, ptr(4), info.LinearConvKernelDim)
	assert.Equal(t, ptr(16), info.LinearNumKeyHeads)
	assert.Equal(t, ptr(128), info.LinearKeyHeadDim)
	assert.Equal(t, ptr(48), info.LinearNumValueHeads)
	assert.Equal(t, ptr(128), info.LinearValueHeadDim)
	assert.Equal(t, "float32", info.RecurrentStateDtype)

	for _, field := range []string{
		v1.ModelInfoFieldLinearConvKernelDim,
		v1.ModelInfoFieldLinearNumKeyHeads,
		v1.ModelInfoFieldLinearKeyHeadDim,
		v1.ModelInfoFieldLinearNumValueHeads,
		v1.ModelInfoFieldLinearValueHeadDim,
		v1.ModelInfoFieldRecurrentStateDtype,
	} {
		assert.NotContains(t, info.MissingFields, field)
	}

	// The recurrent state is routinely held wider than the weights, so the weight
	// dtype does not stand in for it and its absence is a gap.
	noDtype := ParseConfig([]byte(`{"num_hidden_layers":1,"linear_num_key_heads":16,"torch_dtype":"bfloat16"}`))

	assert.Equal(t, "bfloat16", noDtype.ParameterDtype)
	assert.Empty(t, noDtype.RecurrentStateDtype)
	assert.Contains(t, noDtype.MissingFields, v1.ModelInfoFieldRecurrentStateDtype)

	// Declaring linear layers and then not describing them names every width the
	// reader would have needed.
	undescribed := ParseConfig([]byte(`{"num_hidden_layers":2,"layer_types":["linear_attention","full_attention"]}`))

	for _, field := range []string{
		v1.ModelInfoFieldLinearConvKernelDim,
		v1.ModelInfoFieldLinearNumKeyHeads,
		v1.ModelInfoFieldLinearKeyHeadDim,
		v1.ModelInfoFieldLinearNumValueHeads,
		v1.ModelInfoFieldLinearValueHeadDim,
		v1.ModelInfoFieldRecurrentStateDtype,
	} {
		assert.Contains(t, undescribed.MissingFields, field)
	}

	// An ordinary attention checkpoint has no such layer and therefore no gap.
	plain := ParseConfig([]byte(`{"num_hidden_layers":28,"num_key_value_heads":4,"head_dim":128}`))

	assert.Nil(t, plain.LinearConvKernelDim)
	assert.NotContains(t, plain.MissingFields, v1.ModelInfoFieldLinearConvKernelDim)
	assert.NotContains(t, plain.MissingFields, v1.ModelInfoFieldRecurrentStateDtype)
}

// compress_ratios is passed through with its length intact, because the surplus
// over num_hidden_layers is the only statement the checkpoint makes about how
// many draft modules it holds.
func TestParseConfig_CompressRatiosKeepTheirLength(t *testing.T) {
	info := ParseConfig([]byte(`{"num_hidden_layers":4,"compress_ratios":[0,128,4,128,0,0,0],` +
		`"index_head_dim":128,"index_n_heads":64,"index_topk":1024,"num_nextn_predict_layers":1}`))

	assert.Equal(t, []int{0, 128, 4, 128, 0, 0, 0}, info.CompressRatios)
	assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldCompressRatios])
	assert.Equal(t, ptr(128), info.IndexHeadDim)
	assert.Equal(t, ptr(64), info.IndexNumHeads)
	assert.Equal(t, ptr(1024), info.IndexTopK)

	// A schedule with no indexer width is a gap: index_head_dim is what an
	// indexing layer caches per selected token, so the byte count needs it.
	noWidth := ParseConfig([]byte(`{"num_hidden_layers":2,"compress_ratios":[4,128]}`))

	assert.Contains(t, noWidth.MissingFields, v1.ModelInfoFieldIndexHeadDim)

	// index_n_heads and index_topk size the query side and the selection budget,
	// not the cache, so their absence is never a gap.
	assert.NotContains(t, noWidth.MissingFields, v1.ModelInfoFieldIndexNumHeads)
	assert.NotContains(t, noWidth.MissingFields, v1.ModelInfoFieldIndexTopK)

	plain := ParseConfig([]byte(`{"num_hidden_layers":28,"num_key_value_heads":4,"head_dim":128}`))

	assert.Nil(t, plain.CompressRatios)
	assert.NotContains(t, plain.MissingFields, v1.ModelInfoFieldCompressRatios)
	assert.NotContains(t, plain.MissingFields, v1.ModelInfoFieldIndexHeadDim)
}

// Both spellings of the draft-module layer count read into one field, and it is
// reported as what both of them mean — layers per module, not a module count.
func TestParseConfig_ReadsBothMTPSpellings(t *testing.T) {
	deepseek := ParseConfig([]byte(`{"num_hidden_layers":61,"num_nextn_predict_layers":1}`))
	qwen := ParseConfig([]byte(`{"num_hidden_layers":64,"mtp_num_hidden_layers":1}`))

	assert.Equal(t, ptr(1), deepseek.MTPNumLayers)
	assert.Equal(t, ptr(1), qwen.MTPNumLayers)
	assert.Equal(t, v1.ModelInfoSourceAuto, deepseek.FieldSources[v1.ModelInfoFieldMTPNumLayers])

	// Where a config carries both, they state the same quantity and either answer
	// is the same answer; DeepSeek's spelling is read first.
	both := ParseConfig([]byte(`{"num_hidden_layers":8,"num_nextn_predict_layers":2,"mtp_num_hidden_layers":2}`))

	assert.Equal(t, ptr(2), both.MTPNumLayers)

	// No draft module is an answer, not a gap.
	none := ParseConfig([]byte(`{"num_hidden_layers":28}`))

	assert.Nil(t, none.MTPNumLayers)
	assert.NotContains(t, none.MissingFields, v1.ModelInfoFieldMTPNumLayers)
}

// Every key this package reads has to work at both levels of a composite
// config.json, because which level a key sits at is a fact about the wrapper
// rather than about the model. The Qwen3.5-generation checkpoints put all of
// them under text_config while DeepSeek V4 puts all of them at the top, so a
// parser that handled only one layout would report an empty shape for a whole
// model generation — which is the bug this pins shut, from both sides.
func TestParseConfig_HybridKeysReadTheSameFlatOrNested(t *testing.T) {
	const shape = `"num_hidden_layers":4,"num_attention_heads":24,"num_key_value_heads":4,` +
		`"head_dim":256,"max_position_embeddings":262144,"dtype":"bfloat16",` +
		`"layer_types":["linear_attention","linear_attention","linear_attention","full_attention"],` +
		`"linear_conv_kernel_dim":4,"linear_num_key_heads":16,"linear_key_head_dim":128,` +
		`"linear_num_value_heads":48,"linear_value_head_dim":128,"mamba_ssm_dtype":"float32",` +
		`"sliding_window":128,"compress_ratios":[128,4,128,4],"index_head_dim":128,` +
		`"index_n_heads":64,"index_topk":1024,"mtp_num_hidden_layers":1`

	flat := ParseConfig([]byte(`{"architectures":["X"],` + shape + `}`))
	nested := ParseConfig([]byte(`{"architectures":["X"],"text_config":{` + shape + `}}`))

	assert.Equal(t, flat, nested)

	// And the values really are populated, so that an equality of two empty
	// results cannot pass for agreement.
	assert.Equal(t, ptr(4), flat.LinearConvKernelDim)
	assert.Equal(t, ptr(48), flat.LinearNumValueHeads)
	assert.Equal(t, "float32", flat.RecurrentStateDtype)
	assert.Equal(t, ptr(128), flat.SlidingWindow)
	assert.Equal(t, []int{128, 4, 128, 4}, flat.CompressRatios)
	assert.Equal(t, ptr(1), flat.MTPNumLayers)
	assert.Empty(t, flat.MissingFields)
}

// The wrapper's own keys only fill gaps the language-model section leaves. A
// composite that states a shape key at both levels has to report the inner one:
// the outer value can just as well describe a vision tower.
func TestParseConfig_NestedHybridKeysOutrankTheWrapper(t *testing.T) {
	info := ParseConfig([]byte(`{"architectures":["X"],"sliding_window":4096,"linear_num_value_heads":8,` +
		`"mamba_ssm_dtype":"bfloat16","text_config":{"num_hidden_layers":2,` +
		`"layer_types":["sliding_attention","full_attention"],"sliding_window":128,` +
		`"linear_num_value_heads":48,"mamba_ssm_dtype":"float32","linear_conv_kernel_dim":4,` +
		`"linear_num_key_heads":16,"linear_key_head_dim":128,"linear_value_head_dim":128}}`))

	assert.Equal(t, ptr(128), info.SlidingWindow)
	assert.Equal(t, ptr(48), info.LinearNumValueHeads)
	assert.Equal(t, "float32", info.RecurrentStateDtype)
}

// A flat config.json must keep parsing exactly as it did before the descent into
// text_config existed. Sweeping the fixtures makes that a property of every flat
// checkpoint on file rather than of one hand-picked example: re-parsing each one
// with its keys moved down a level has to produce the same ModelInfo.
func TestParseConfig_FlatFixturesSurviveBeingNested(t *testing.T) {
	for _, fixture := range fixtureDirs(t) {
		t.Run(fixture, func(t *testing.T) {
			raw, err := os.ReadFile(filepath.Join("testdata", fixture, ConfigFileName))
			require.NoError(t, err)

			var stated map[string]json.RawMessage
			if err := json.Unmarshal(raw, &stated); err != nil {
				t.Skip("the malformed fixture has no keys to move")
			}

			if _, nested := stated["text_config"]; nested {
				t.Skip("already composite")
			}

			// The wrapper repeats architectures because that is what a real
			// composite does — the name of the served model stays at the top —
			// and it is the one key the outer level wins outright.
			outer := map[string]any{"text_config": json.RawMessage(raw)}
			if names, ok := stated["architectures"]; ok {
				outer["architectures"] = json.RawMessage(names)
			}

			wrapped, err := json.Marshal(outer)
			require.NoError(t, err)

			assert.Equal(t, ParseConfig(raw), ParseConfig(wrapped))
		})
	}
}
