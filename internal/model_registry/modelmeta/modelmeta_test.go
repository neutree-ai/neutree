package modelmeta

import (
	"bytes"
	"encoding/binary"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// goldenFileName holds the ModelInfo its sibling config.json is expected to
// parse into, serialized the way the API serves it. The expectation lives beside
// the input on purpose: adding a case is adding a directory, with no Go code to
// touch and nothing to keep in step across two files.
const goldenFileName = "expected.json"

// updateGolden rewrites every golden from what Parse currently returns:
//
//	go test ./internal/model_registry/modelmeta -run TestParse_MatchesGolden -update
//
// It is off by default, and TestMain fails any run that sets it, so a rewrite
// can never be mistaken for a passing verification — read the resulting diff,
// then re-run without the flag to actually check it. Pass the package path
// rather than ./...: the flag only exists in this package's test binary.
var updateGolden = flag.Bool("update", false, "rewrite the modelmeta golden files from the current parser output")

// TestMain turns a golden rewrite into a failure. Blessing the current output
// and reporting success in the same run is what makes golden tests rot: on CI it
// would be a green build asserting nothing, and locally it hides the moment an
// expectation changed.
func TestMain(m *testing.M) {
	flag.Parse()

	code := m.Run()

	if *updateGolden && code == 0 {
		fmt.Fprintf(os.Stderr, "-update rewrote the golden files; review the diff and re-run without -update to verify them\n")

		code = 1
	}

	os.Exit(code)
}

// The values every fixture parses into, pinned whole. Comparing the serialized
// form rather than field by field also pins what a client actually receives:
// which keys are omitted, and the order the parser reports MissingFields in.
func TestParse_MatchesGolden(t *testing.T) {
	for _, fixture := range fixtureDirs(t) {
		t.Run(fixture, func(t *testing.T) {
			golden := filepath.Join("testdata", fixture, goldenFileName)
			got := marshalGolden(t, Parse(filepath.Join("testdata", fixture)))

			if *updateGolden {
				require.NoError(t, os.WriteFile(golden, got, 0o600))

				return
			}

			want, err := os.ReadFile(golden)
			require.NoError(t, err, "fixture %q has no %s — generate it with -update", fixture, goldenFileName)

			assert.Equal(t, string(want), string(got))
		})
	}
}

// fixtureDirs names every checkpoint fixture. Both sweeps walk the same set, so
// a new directory is picked up by all of them at once.
func fixtureDirs(t *testing.T) []string {
	t.Helper()

	entries, err := os.ReadDir("testdata")
	require.NoError(t, err)

	var dirs []string

	for _, entry := range entries {
		if entry.IsDir() {
			dirs = append(dirs, entry.Name())
		}
	}

	require.NotEmpty(t, dirs)

	return dirs
}

// marshalGolden renders info in the one canonical form goldens are stored in, so
// that a byte comparison is a comparison of values and not of formatting.
func marshalGolden(t *testing.T, info *v1.ModelInfo) []byte {
	t.Helper()

	raw, err := json.MarshalIndent(info, "", "  ")
	require.NoError(t, err)

	return append(raw, '\n')
}

// A checkpoint whose config.json cannot be read tells us nothing at all — the
// parse is anchored on that file — and it must still not be an error, because a
// model without one is a legitimate thing to list.
func TestParse_UnreadableConfigYieldsNothingKnown(t *testing.T) {
	tests := []struct {
		name string
		dir  string
	}{
		{name: "malformed config.json", dir: filepath.Join("testdata", "invalid")},
		{name: "no config.json", dir: t.TempDir()},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := Parse(tt.dir)

			assert.Equal(t, &v1.ModelInfo{MissingFields: allFields}, info)
		})
	}
}

// The parameter count is read out of the safetensors headers, so it has to come
// out exact for both the single-file and the sharded layout.
func TestParse_ParameterCountFromSafetensors(t *testing.T) {
	const config = `{"architectures":["LlamaForCausalLM"],"hidden_size":8,"num_attention_heads":2,` +
		`"num_hidden_layers":1,"num_key_value_heads":2,"max_position_embeddings":128,"torch_dtype":"float16"}`

	tests := []struct {
		name   string
		shards map[string]map[string][]int64
		want   string
	}{
		{
			name: "single file",
			shards: map[string]map[string][]int64{
				"model.safetensors": {
					"model.embed_tokens.weight": {128, 8},
					"model.layers.0.mlp.weight": {8, 16},
					"model.norm.weight":         {8},
				},
			},
			// 1024 + 128 + 8
			want: "1160",
		},
		{
			name: "sharded",
			shards: map[string]map[string][]int64{
				"model-00001-of-00002.safetensors": {
					"model.embed_tokens.weight": {128, 8},
				},
				"model-00002-of-00002.safetensors": {
					"model.layers.0.mlp.weight": {8, 16},
					"model.norm.weight":         {8},
					// A scalar tensor has an empty shape and counts as one element.
					"model.scale": {},
				},
			},
			// 1024 + 128 + 8 + 1
			want: "1161",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(config), 0o600))

			for name, tensors := range tt.shards {
				writeSafetensors(t, filepath.Join(dir, name), tensors)
			}

			info := Parse(dir)

			assert.Equal(t, tt.want, info.ParameterCount)
			assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldParameterCount])
			assert.NotContains(t, info.MissingFields, v1.ModelInfoFieldParameterCount)
		})
	}
}

// Only safetensors is parsed. A pickle checkpoint or a GGUF file leaves the
// count unknown rather than estimated from the file size.
func TestParse_UnsupportedWeightFormatsLeaveCountMissing(t *testing.T) {
	const config = `{"architectures":["LlamaForCausalLM"],"num_hidden_layers":1}`

	for _, weights := range []string{"pytorch_model.bin", "model.gguf"} {
		t.Run(weights, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(config), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, weights), bytes.Repeat([]byte{0x42}, 4096), 0o600))

			info := Parse(dir)

			assert.Empty(t, info.ParameterCount)
			assert.Contains(t, info.MissingFields, v1.ModelInfoFieldParameterCount)
		})
	}
}

// A truncated or corrupt shard must not be reported as a partial count: a wrong
// parameter count is worse than an absent one.
func TestParse_CorruptSafetensorsLeavesCountMissing(t *testing.T) {
	const config = `{"architectures":["LlamaForCausalLM"],"num_hidden_layers":1}`

	tests := []struct {
		name    string
		content []byte
	}{
		{name: "truncated length prefix", content: []byte{0x01, 0x02}},
		{name: "header longer than file", content: append(u64le(1<<20), []byte(`{}`)...)},
		{name: "header is not json", content: append(u64le(5), []byte(`nope!`)...)},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			require.NoError(t, os.WriteFile(filepath.Join(dir, ConfigFileName), []byte(config), 0o600))
			require.NoError(t, os.WriteFile(filepath.Join(dir, "model.safetensors"), tt.content, 0o600))

			info := Parse(dir)

			assert.Empty(t, info.ParameterCount)
			assert.Contains(t, info.MissingFields, v1.ModelInfoFieldParameterCount)
		})
	}
}

// The one inference this package must never make. A directory named after a
// well-advertised model, holding nothing that states any of it, has to come back
// empty — not filled in from the name.
func TestParse_NeverInfersFromNames(t *testing.T) {
	for _, name := range []string{
		"Qwen3-235B-A22B-Instruct-2507-FP8",
		"llama-3.1-70b-instruct-awq-int4",
		"mixtral-8x7b-v0.1",
	} {
		t.Run(name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), name)
			require.NoError(t, os.MkdirAll(dir, 0o755))

			assert.Equal(t, &v1.ModelInfo{MissingFields: allFields}, Parse(dir))
		})
	}
}

// A composite config carries shape keys at more than one level. The section the
// language model is described in is the one that has to win: a wrapper's own
// keys can belong to the vision tower, and reading a head count off the wrong
// tower produces a number that looks measured and is wrong.
func TestParseConfig_CompositeConfigPrefersTheLanguageModelSection(t *testing.T) {
	const config = `{
		"architectures": ["Qwen3_5MoeForConditionalGeneration"],
		"num_hidden_layers": 99,
		"torch_dtype": "float32",
		"text_config": {
			"num_hidden_layers": 40,
			"num_attention_heads": 16,
			"head_dim": 256,
			"max_position_embeddings": 262144,
			"num_experts": 256
		},
		"vision_config": {"num_hidden_layers": 27, "hidden_size": 1152, "torch_dtype": "float16"}
	}`

	info := ParseConfig([]byte(config))

	// From the text section, not from the wrapper's stray key.
	assert.Equal(t, 40, *info.NumHiddenLayers)
	// The wrapper names the model that is served, so the architecture stays its.
	assert.Equal(t, "Qwen3_5MoeForConditionalGeneration", info.Architecture)
	// Stated only by the wrapper: the outer level fills the gaps, it just does
	// not overrule.
	assert.Equal(t, "float32", info.ParameterDtype)
	// vision_config is not a section the parser descends into at all.
	assert.NotEqual(t, 1152, deref(info.HeadDim))
	assert.Equal(t, 256, *info.HeadDim)
	assert.True(t, *info.IsMoE)
	assert.Equal(t, 256, *info.NumExperts)
}

// transformers renamed torch_dtype to dtype in 4.56, so both spellings are in
// the wild and a checkpoint saved by a current release states only the new one.
func TestParseConfig_ReadsBothDtypeSpellings(t *testing.T) {
	tests := []struct {
		name   string
		config string
		want   string
	}{
		{name: "torch_dtype", config: `{"torch_dtype":"bfloat16"}`, want: "bfloat16"},
		{name: "dtype", config: `{"dtype":"bfloat16"}`, want: "bfloat16"},
		{name: "both", config: `{"torch_dtype":"float16","dtype":"bfloat16"}`, want: "float16"},
		{name: "neither", config: `{"num_hidden_layers":1}`, want: ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ParseConfig([]byte(tt.config))

			assert.Equal(t, tt.want, info.ParameterDtype)

			if tt.want == "" {
				assert.Contains(t, info.MissingFields, v1.ModelInfoFieldParameterDtype)

				return
			}

			assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldParameterDtype])
		})
	}
}

// The MLA widths decide which cache layout a reader may assume, and a
// checkpoint that uses MLA also states num_key_value_heads and head_dim. So they
// have to be reported when stated and left alone when not: inventing them makes
// a head-based model look latent, and dropping them makes a latent model look
// head-based.
func TestParseConfig_LatentAttentionIsReportedOnlyWhenStated(t *testing.T) {
	tests := []struct {
		name        string
		config      string
		wantRank    *int
		wantRope    *int
		wantMissing []string
	}{
		{
			name:   "head-based config states neither",
			config: `{"num_attention_heads":28,"num_key_value_heads":4,"head_dim":128}`,
		},
		{
			name:     "latent config states both",
			config:   `{"num_attention_heads":128,"num_key_value_heads":128,"kv_lora_rank":512,"qk_rope_head_dim":64}`,
			wantRank: ptr(512),
			wantRope: ptr(64),
		},
		{
			name:        "half a latent config leaves the other half missing",
			config:      `{"num_attention_heads":16,"num_key_value_heads":16,"kv_lora_rank":512}`,
			wantRank:    ptr(512),
			wantMissing: []string{v1.ModelInfoFieldQKRopeHeadDim},
		},
		{
			// A rope width with no rank at all is still treated as a half-stated
			// MLA layer, because that is what it looks like and there is no other
			// reading of it. The refusal this produces downstream is the point.
			name:        "a rope width with no rank leaves the rank missing",
			config:      `{"num_attention_heads":16,"num_key_value_heads":16,"qk_rope_head_dim":64}`,
			wantRope:    ptr(64),
			wantMissing: []string{v1.ModelInfoFieldKVLoraRank},
		},
		{
			// DeepSeek V4 states the same rope width and no rank, and is not
			// half-stated: compress_ratios says it is the V4 layout, whose cache
			// is head_dim wide with the rope slice inside it. Reporting
			// kv_lora_rank missing here invents a gap and refuses a model that
			// has told us everything.
			name: "the V4 layout states a rope width and no rank without a gap",
			config: `{"num_attention_heads":128,"num_key_value_heads":1,"head_dim":512,` +
				`"qk_rope_head_dim":64,"num_hidden_layers":2,"compress_ratios":[128,4],` +
				`"index_head_dim":128,"sliding_window":128}`,
			wantRope: ptr(64),
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			info := ParseConfig([]byte(tt.config))

			assert.Equal(t, tt.wantRank, info.KVLoraRank)
			assert.Equal(t, tt.wantRope, info.QKRopeHeadDim)

			for _, field := range []string{v1.ModelInfoFieldKVLoraRank, v1.ModelInfoFieldQKRopeHeadDim} {
				assert.Equal(t, slices.Contains(tt.wantMissing, field), slices.Contains(info.MissingFields, field),
					"unexpected missing_fields entry for %q", field)
			}

			if tt.wantRank != nil {
				assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldKVLoraRank])
			}
		})
	}
}

// layer_types is passed through as the file states it. Names outside the
// vocabulary of any architecture we have seen must survive unchanged, because
// what a reader does with the list — deciding whether every layer is alike —
// does not depend on recognizing the names.
func TestParseConfig_LayerTypesArePassedThroughVerbatim(t *testing.T) {
	info := ParseConfig([]byte(`{"num_hidden_layers":4,"layer_types":["mamba","attention","mamba","something_new"]}`))

	assert.Equal(t, []string{"mamba", "attention", "mamba", "something_new"}, info.LayerTypes)
	assert.Equal(t, v1.ModelInfoSourceAuto, info.FieldSources[v1.ModelInfoFieldLayerTypes])

	// A config that says nothing about its layers is not evidence that they are
	// uniform, so the field is neither populated nor reported as a known gap.
	plain := ParseConfig([]byte(`{"num_hidden_layers":4}`))

	assert.Nil(t, plain.LayerTypes)
	assert.NotContains(t, plain.MissingFields, v1.ModelInfoFieldLayerTypes)
}

// Nesting is bounded, and a config that nests past the bound still parses into
// whatever the levels above it stated rather than hanging or panicking.
func TestParseConfig_NestingIsBounded(t *testing.T) {
	config := `{"architectures":["Deep"],"num_hidden_layers":1`
	for range 8 {
		config += `,"text_config":{"num_hidden_layers":2`
	}

	config += strings.Repeat("}", 8) + "}"

	info := ParseConfig([]byte(config))

	assert.Equal(t, "Deep", info.Architecture)
	assert.NotNil(t, info.NumHiddenLayers)
}

func ptr[T any](v T) *T {
	return &v
}

func deref(v *int) int {
	if v == nil {
		return 0
	}

	return *v
}

// backingKeys names the config.json keys a field may legitimately be read from.
// A nil entry means the field does not come from a config key: is_moe is settled
// by the config being readable at all, and parameter_count comes from the weight
// files.
var backingKeys = map[string][]string{
	v1.ModelInfoFieldArchitecture:          {"architectures"},
	v1.ModelInfoFieldNumHiddenLayers:       {"num_hidden_layers"},
	v1.ModelInfoFieldNumAttentionHeads:     {"num_attention_heads"},
	v1.ModelInfoFieldNumKeyValueHeads:      {"num_key_value_heads"},
	v1.ModelInfoFieldHeadDim:               {"head_dim", "hidden_size"},
	v1.ModelInfoFieldKVLoraRank:            {"kv_lora_rank"},
	v1.ModelInfoFieldQKRopeHeadDim:         {"qk_rope_head_dim"},
	v1.ModelInfoFieldLayerTypes:            {"layer_types"},
	v1.ModelInfoFieldSlidingWindow:         {"sliding_window"},
	v1.ModelInfoFieldLinearConvKernelDim:   {"linear_conv_kernel_dim"},
	v1.ModelInfoFieldLinearNumKeyHeads:     {"linear_num_key_heads"},
	v1.ModelInfoFieldLinearKeyHeadDim:      {"linear_key_head_dim"},
	v1.ModelInfoFieldLinearNumValueHeads:   {"linear_num_value_heads"},
	v1.ModelInfoFieldLinearValueHeadDim:    {"linear_value_head_dim"},
	v1.ModelInfoFieldRecurrentStateDtype:   {"mamba_ssm_dtype"},
	v1.ModelInfoFieldCompressRatios:        {"compress_ratios"},
	v1.ModelInfoFieldIndexNumHeads:         {"index_n_heads"},
	v1.ModelInfoFieldIndexHeadDim:          {"index_head_dim"},
	v1.ModelInfoFieldIndexTopK:             {"index_topk"},
	v1.ModelInfoFieldMTPNumLayers:          {"num_nextn_predict_layers", "mtp_num_hidden_layers"},
	v1.ModelInfoFieldMaxPositionEmbeddings: {"max_position_embeddings"},
	v1.ModelInfoFieldContextLength:         {"max_position_embeddings"},
	v1.ModelInfoFieldParameterDtype:        {"torch_dtype", "dtype"},
	v1.ModelInfoFieldNumExperts:            {"num_local_experts", "num_experts"},
	v1.ModelInfoFieldNumExpertsPerToken:    {"num_experts_per_tok"},
	v1.ModelInfoFieldQuantizationBits:      {"quantization_config"},
	v1.ModelInfoFieldIsMoE:                 nil,
	v1.ModelInfoFieldParameterCount:        nil,
}

// Sweeping every fixture: a field may only carry a value if the config states a
// key it can come from. Nothing may arrive from the directory name, and none of
// these fixtures ship weights, so the parameter count may never be sourced.
func TestParse_FixturesOnlyPopulateWhatTheConfigStates(t *testing.T) {
	for _, fixture := range fixtureDirs(t) {
		t.Run(fixture, func(t *testing.T) {
			dir := filepath.Join("testdata", fixture)
			info := Parse(dir)

			assertNoFieldBothSetAndMissing(t, info)

			raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
			require.NoError(t, err)

			var stated map[string]any
			if err := json.Unmarshal(raw, &stated); err != nil {
				// The malformed fixture: nothing may be established from it.
				assert.Equal(t, &v1.ModelInfo{MissingFields: allFields}, info)

				return
			}

			assert.NotContains(t, info.FieldSources, v1.ModelInfoFieldParameterCount,
				"parameter_count was populated for a fixture that ships no weight files")

			for field := range info.FieldSources {
				keys, known := backingKeys[field]
				require.True(t, known, "field %q is not in backingKeys — add it there too", field)

				if keys == nil {
					continue
				}

				assert.True(t, statesAnyKey(stated, keys),
					"field %q was populated but the config states none of %v", field, keys)
			}
		})
	}
}

// A composite checkpoint states the language model's shape one level down, so
// the search follows the same nesting the parser does. Only the sections the
// parser descends into count: a value that could only have come from
// vision_config is a value read off the wrong tower, and this has to keep
// catching that.
func statesAnyKey(stated map[string]any, keys []string) bool {
	for _, key := range keys {
		if _, ok := stated[key]; ok {
			return true
		}
	}

	for _, section := range nestedConfigKeys {
		nested, ok := stated[section].(map[string]any)
		if ok && statesAnyKey(nested, keys) {
			return true
		}
	}

	return false
}

func assertNoFieldBothSetAndMissing(t *testing.T, info *v1.ModelInfo) {
	t.Helper()

	for _, field := range info.MissingFields {
		assert.NotContains(t, info.FieldSources, field,
			"field %q is reported both missing and sourced", field)
	}

	sorted := append([]string(nil), info.MissingFields...)
	sort.Strings(sorted)

	for i := 1; i < len(sorted); i++ {
		assert.NotEqual(t, sorted[i-1], sorted[i], "field %q listed twice in missing_fields", sorted[i])
	}
}

func u64le(v uint64) []byte {
	buf := make([]byte, 8)
	binary.LittleEndian.PutUint64(buf, v)

	return buf
}

// writeSafetensors builds a container matching the published format: an 8-byte
// little-endian header length, that many bytes of JSON header, then the data
// region the offsets point into.
func writeSafetensors(t *testing.T, path string, tensors map[string][]int64) {
	t.Helper()

	header := map[string]any{"__metadata__": map[string]string{"format": "pt"}}

	var offset int64

	for name, shape := range tensors {
		elements := int64(1)
		for _, dim := range shape {
			elements *= dim
		}

		size := elements * 2 // every fixture tensor is F16

		header[name] = map[string]any{
			"dtype":        "F16",
			"shape":        shape,
			"data_offsets": []int64{offset, offset + size},
		}
		offset += size
	}

	raw, err := json.Marshal(header)
	require.NoError(t, err)

	buf := new(bytes.Buffer)
	buf.Write(u64le(uint64(len(raw))))
	buf.Write(raw)
	buf.Write(make([]byte, offset))

	require.NoError(t, os.WriteFile(path, buf.Bytes(), 0o600))
}
