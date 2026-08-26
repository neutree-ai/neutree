// Package modelmeta reads what a Hugging Face style checkpoint directory states
// about itself: the shape parameters in config.json and the exact parameter
// count in the safetensors headers.
//
// It only ever reports what the files say. There is deliberately no inference
// from a directory or repository name — a checkpoint called "qwen3-8b" is not
// evidence of anything, and a wrong parameter count is worse than an absent one.
// Anything the files do not establish comes back in ModelInfo.MissingFields.
package modelmeta

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ConfigFileName is the checkpoint config every parse is anchored on.
const ConfigFileName = "config.json"

// allFields is every field this package can establish, in the order it reports
// them. A checkpoint with no readable config.json is unreadable as a whole, so
// this doubles as the "nothing is known" MissingFields list.
var allFields = []string{
	v1.ModelInfoFieldArchitecture,
	v1.ModelInfoFieldNumHiddenLayers,
	v1.ModelInfoFieldNumAttentionHeads,
	v1.ModelInfoFieldNumKeyValueHeads,
	v1.ModelInfoFieldHeadDim,
	v1.ModelInfoFieldKVLoraRank,
	v1.ModelInfoFieldQKRopeHeadDim,
	v1.ModelInfoFieldLayerTypes,
	v1.ModelInfoFieldSlidingWindow,
	v1.ModelInfoFieldLinearConvKernelDim,
	v1.ModelInfoFieldLinearNumKeyHeads,
	v1.ModelInfoFieldLinearKeyHeadDim,
	v1.ModelInfoFieldLinearNumValueHeads,
	v1.ModelInfoFieldLinearValueHeadDim,
	v1.ModelInfoFieldRecurrentStateDtype,
	v1.ModelInfoFieldCompressRatios,
	v1.ModelInfoFieldIndexNumHeads,
	v1.ModelInfoFieldIndexHeadDim,
	v1.ModelInfoFieldIndexTopK,
	v1.ModelInfoFieldMTPNumLayers,
	v1.ModelInfoFieldMaxPositionEmbeddings,
	v1.ModelInfoFieldContextLength,
	v1.ModelInfoFieldParameterDtype,
	v1.ModelInfoFieldIsMoE,
	v1.ModelInfoFieldNumExperts,
	v1.ModelInfoFieldNumExpertsPerToken,
	v1.ModelInfoFieldQuantizationBits,
	v1.ModelInfoFieldParameterCount,
}

// configFields is everything config.json alone can establish: all of the above
// except the parameter count, which is only in the weight files.
var configFields = allFields[:len(allFields)-1]

// hfConfig is the subset of config.json this package reads. Every scalar is a
// pointer so that a key present with value 0 is distinguishable from an absent
// key — the difference between "the checkpoint says zero" and "we don't know".
//
// One value of it describes one model, which for a composite checkpoint means
// the merge of the section describing the language model with the levels above
// it — see decodeConfig. The rest of the package therefore reads a single flat
// config and never has to know which layout it came from.
type hfConfig struct {
	Architectures         []string            `json:"architectures"`
	NumHiddenLayers       *int                `json:"num_hidden_layers"`
	NumAttentionHeads     *int                `json:"num_attention_heads"`
	NumKeyValueHeads      *int                `json:"num_key_value_heads"`
	HeadDim               *int                `json:"head_dim"`
	KVLoraRank            *int                `json:"kv_lora_rank"`
	QKRopeHeadDim         *int                `json:"qk_rope_head_dim"`
	LayerTypes            []string            `json:"layer_types"`
	SlidingWindow         *int                `json:"sliding_window"`
	UseSlidingWindow      *bool               `json:"use_sliding_window"`
	LinearConvKernelDim   *int                `json:"linear_conv_kernel_dim"`
	LinearNumKeyHeads     *int                `json:"linear_num_key_heads"`
	LinearKeyHeadDim      *int                `json:"linear_key_head_dim"`
	LinearNumValueHeads   *int                `json:"linear_num_value_heads"`
	LinearValueHeadDim    *int                `json:"linear_value_head_dim"`
	MambaSSMDtype         string              `json:"mamba_ssm_dtype"`
	CompressRatios        []int               `json:"compress_ratios"`
	IndexNumHeads         *int                `json:"index_n_heads"`
	IndexHeadDim          *int                `json:"index_head_dim"`
	IndexTopK             *int                `json:"index_topk"`
	NumNextNPredictLayers *int                `json:"num_nextn_predict_layers"`
	MTPNumHiddenLayers    *int                `json:"mtp_num_hidden_layers"`
	HiddenSize            *int                `json:"hidden_size"`
	MaxPositionEmbeddings *int                `json:"max_position_embeddings"`
	TorchDtype            string              `json:"torch_dtype"`
	Dtype                 string              `json:"dtype"`
	NumLocalExperts       *int                `json:"num_local_experts"`
	NumExperts            *int                `json:"num_experts"`
	NumExpertsPerTok      *int                `json:"num_experts_per_tok"`
	QuantizationConfig    *quantizationConfig `json:"quantization_config"`
}

// dtype reports the weight dtype the config states. transformers renamed the key
// from torch_dtype to dtype in 4.56 and writes only the new spelling now, so a
// parser that reads one of the two reports the precision of old checkpoints and
// nothing about current ones. Both are read, the older spelling first, because
// where a config carries both it is the one transformers itself still honours.
func (c *hfConfig) dtype() string {
	if c.TorchDtype != "" {
		return c.TorchDtype
	}

	return c.Dtype
}

// quantizationConfig covers the shapes transformers writes for the quantizers
// that carry a width: GPTQ and AWQ state it as a bit count, FP8 states only the
// method and the width is implied by the name.
type quantizationConfig struct {
	Bits        *int   `json:"bits"`
	WBit        *int   `json:"w_bit"`
	QuantMethod string `json:"quant_method"`
}

// bits reports the weight width in bits, or nil when this config does not state
// one in a form we recognise.
func (q *quantizationConfig) bits() *int {
	switch {
	case q.Bits != nil:
		return q.Bits
	case q.WBit != nil:
		return q.WBit
	case q.QuantMethod == "fp8":
		width := 8

		return &width
	default:
		return nil
	}
}

// Parse reports what the checkpoint in dir states about itself. It never fails:
// an absent, unreadable or malformed config.json yields an empty ModelInfo whose
// MissingFields names everything, because that file is what the rest of the
// parse is anchored on.
func Parse(dir string) *v1.ModelInfo {
	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		raw = nil
	}

	info := ParseConfig(raw)

	if decodeConfig(raw) == nil {
		// Nothing is anchored, so the count is not read either. Appended here
		// rather than inside ParseConfig so that the missing-field list stays in
		// the declared order.
		info.MarkFieldMissing(v1.ModelInfoFieldParameterCount)

		return info
	}

	applyParameterCount(info, dir)

	return info
}

// ParseConfig reports what a config.json states about a checkpoint, without
// touching the weight files.
//
// It exists because a public hub serves that one file over HTTP while the
// weights stay where they are: the shape of a model — layers, heads, KV heads,
// head_dim, experts, quantization — is readable without downloading a
// checkpoint, and it is the same file with the same meaning either way, so it is
// read by the same code. The parameter count is deliberately not its business:
// that lives in the weight files, and a caller that has none has to say so
// itself.
//
// Like Parse, it never fails and never guesses. Absent or malformed input yields
// an empty ModelInfo naming every field it could not establish.
func ParseConfig(raw []byte) *v1.ModelInfo {
	info := &v1.ModelInfo{}

	cfg := decodeConfig(raw)
	if cfg == nil {
		info.MissingFields = append(info.MissingFields, configFields...)

		return info
	}

	applyConfig(info, cfg)

	return info
}

// nestedConfigKeys names, in order, the keys a composite config.json puts the
// language model's own config under. A multimodal or speech checkpoint is a
// wrapper: architectures stays at the top while layers, heads and experts move a
// level down, so reading only the top level reports a model whose name is known
// and whose every shape parameter is not.
//
// This is a list of keys rather than of architectures on purpose. The file
// states its own layout, and switching on the architecture string would be the
// guessing this package refuses to do everywhere else — a new
// "…ForConditionalGeneration" lands every few weeks, and each one would be
// unreadable until someone added it here.
var nestedConfigKeys = []string{"text_config", "thinker_config", "llm_config"}

// maxNestedConfigDepth bounds the descent. Qwen3-Omni nests two deep
// (thinker_config → text_config), which is the deepest layout in the wild; the
// limit is what stops a hand-written or hostile config from recursing without
// end.
const maxNestedConfigDepth = 3

func decodeConfig(raw []byte) *hfConfig {
	if len(raw) == 0 {
		return nil
	}

	var outer hfConfig
	if err := json.Unmarshal(raw, &outer); err != nil {
		return nil
	}

	merged := decodeNestedConfig(raw, 0)
	if merged == nil {
		return &outer
	}

	merged.fillFrom(&outer)

	// The wrapper names the model that is actually served —
	// Qwen3_5MoeForConditionalGeneration, not the plain causal-LM the text
	// section names — and that is the name a reader is looking for. It is the one
	// field the outer level wins outright.
	if len(outer.Architectures) > 0 {
		merged.Architectures = outer.Architectures
	}

	return merged
}

// decodeNestedConfig returns the language model's own config out of a composite
// config.json, or nil when the file is flat and the top level is all there is.
func decodeNestedConfig(raw []byte, depth int) *hfConfig {
	if depth >= maxNestedConfigDepth {
		return nil
	}

	var sections map[string]json.RawMessage
	if err := json.Unmarshal(raw, &sections); err != nil {
		return nil
	}

	for _, key := range nestedConfigKeys {
		section, ok := sections[key]
		if !ok {
			continue
		}

		var cfg hfConfig
		if err := json.Unmarshal(section, &cfg); err != nil {
			continue
		}

		if deeper := decodeNestedConfig(section, depth+1); deeper != nil {
			deeper.fillFrom(&cfg)
			cfg = *deeper
		}

		return &cfg
	}

	return nil
}

// fillFrom takes from the level above everything this config does not state
// itself.
//
// The nested config wins wherever both state a key. A wrapper's own shape keys
// describe the composite and can just as well belong to the vision tower, while
// the section this was decoded from is unambiguously the language model — so it
// is the authority on the language model's shape, and the outer level only fills
// the gaps it leaves, as Qwen3-Omni's top-level dtype does.
func (c *hfConfig) fillFrom(outer *hfConfig) {
	fillPointer(&c.NumHiddenLayers, outer.NumHiddenLayers)
	fillPointer(&c.NumAttentionHeads, outer.NumAttentionHeads)
	fillPointer(&c.NumKeyValueHeads, outer.NumKeyValueHeads)
	fillPointer(&c.HeadDim, outer.HeadDim)
	fillPointer(&c.KVLoraRank, outer.KVLoraRank)
	fillPointer(&c.QKRopeHeadDim, outer.QKRopeHeadDim)
	fillPointer(&c.SlidingWindow, outer.SlidingWindow)
	fillPointer(&c.UseSlidingWindow, outer.UseSlidingWindow)
	fillPointer(&c.LinearConvKernelDim, outer.LinearConvKernelDim)
	fillPointer(&c.LinearNumKeyHeads, outer.LinearNumKeyHeads)
	fillPointer(&c.LinearKeyHeadDim, outer.LinearKeyHeadDim)
	fillPointer(&c.LinearNumValueHeads, outer.LinearNumValueHeads)
	fillPointer(&c.LinearValueHeadDim, outer.LinearValueHeadDim)
	fillPointer(&c.IndexNumHeads, outer.IndexNumHeads)
	fillPointer(&c.IndexHeadDim, outer.IndexHeadDim)
	fillPointer(&c.IndexTopK, outer.IndexTopK)
	fillPointer(&c.NumNextNPredictLayers, outer.NumNextNPredictLayers)
	fillPointer(&c.MTPNumHiddenLayers, outer.MTPNumHiddenLayers)
	fillPointer(&c.HiddenSize, outer.HiddenSize)
	fillPointer(&c.MaxPositionEmbeddings, outer.MaxPositionEmbeddings)
	fillPointer(&c.NumLocalExperts, outer.NumLocalExperts)
	fillPointer(&c.NumExperts, outer.NumExperts)
	fillPointer(&c.NumExpertsPerTok, outer.NumExpertsPerTok)
	fillPointer(&c.QuantizationConfig, outer.QuantizationConfig)

	if c.TorchDtype == "" {
		c.TorchDtype = outer.TorchDtype
	}

	if c.Dtype == "" {
		c.Dtype = outer.Dtype
	}

	if c.MambaSSMDtype == "" {
		c.MambaSSMDtype = outer.MambaSSMDtype
	}

	if len(c.LayerTypes) == 0 {
		c.LayerTypes = outer.LayerTypes
	}

	if len(c.CompressRatios) == 0 {
		c.CompressRatios = outer.CompressRatios
	}

	if len(c.Architectures) == 0 {
		c.Architectures = outer.Architectures
	}
}

func fillPointer[T any](dst **T, src *T) {
	if *dst == nil {
		*dst = src
	}
}

func applyConfig(info *v1.ModelInfo, cfg *hfConfig) {
	if len(cfg.Architectures) > 0 && cfg.Architectures[0] != "" {
		info.Architecture = cfg.Architectures[0]
		info.SetFieldSource(v1.ModelInfoFieldArchitecture, v1.ModelInfoSourceAuto)
	} else {
		info.MarkFieldMissing(v1.ModelInfoFieldArchitecture)
	}

	info.NumHiddenLayers = cfg.NumHiddenLayers
	recordAuto(info, v1.ModelInfoFieldNumHiddenLayers, cfg.NumHiddenLayers != nil)

	info.NumAttentionHeads = cfg.NumAttentionHeads
	recordAuto(info, v1.ModelInfoFieldNumAttentionHeads, cfg.NumAttentionHeads != nil)

	// A checkpoint that omits num_key_value_heads is conventionally MHA, i.e.
	// equal to num_attention_heads. That convention is not applied here: it would
	// be a second derivation, and reporting the field as unknown is honest.
	info.NumKeyValueHeads = cfg.NumKeyValueHeads
	recordAuto(info, v1.ModelInfoFieldNumKeyValueHeads, cfg.NumKeyValueHeads != nil)

	applyHeadDim(info, cfg)
	applyLatentAttention(info, cfg)
	applyLayerTypes(info, cfg)
	applySlidingWindow(info, cfg)
	applyLinearAttention(info, cfg)
	applySparseAttention(info, cfg)
	applyMTP(info, cfg)

	info.MaxPositionEmbeddings = cfg.MaxPositionEmbeddings
	recordAuto(info, v1.ModelInfoFieldMaxPositionEmbeddings, cfg.MaxPositionEmbeddings != nil)

	if cfg.MaxPositionEmbeddings != nil {
		info.ContextLength = strconv.Itoa(*cfg.MaxPositionEmbeddings)
	}

	recordAuto(info, v1.ModelInfoFieldContextLength, cfg.MaxPositionEmbeddings != nil)

	info.ParameterDtype = cfg.dtype()
	recordAuto(info, v1.ModelInfoFieldParameterDtype, info.ParameterDtype != "")

	applyExperts(info, cfg)
	applyQuantization(info, cfg)
}

// applyHeadDim resolves the one arithmetic derivation this package allows:
// hidden_size / num_attention_heads when head_dim is absent. That is the
// established Hugging Face convention for the fallback, not a guess.
//
// The division has to come out exact. Integer division would otherwise truncate
// a config where the two do not divide evenly and hand back a wrong head_dim
// carrying derived provenance — worse than reporting nothing, because a value
// that claims to come from the checkpoint gives a reader no way to notice it is
// wrong.
func applyHeadDim(info *v1.ModelInfo, cfg *hfConfig) {
	if cfg.HeadDim != nil {
		info.HeadDim = cfg.HeadDim
		info.SetFieldSource(v1.ModelInfoFieldHeadDim, v1.ModelInfoSourceAuto)

		return
	}

	if cfg.HiddenSize == nil || cfg.NumAttentionHeads == nil || *cfg.NumAttentionHeads <= 0 ||
		*cfg.HiddenSize%*cfg.NumAttentionHeads != 0 {
		info.MarkFieldMissing(v1.ModelInfoFieldHeadDim)

		return
	}

	derived := *cfg.HiddenSize / *cfg.NumAttentionHeads
	info.HeadDim = &derived
	info.SetFieldSource(v1.ModelInfoFieldHeadDim, v1.ModelInfoSourceDerived)
}

// applyLatentAttention reads the two widths that describe an MLA layer's cache.
// A checkpoint that states neither is simply not an MLA model, which is an
// answer rather than a gap, so nothing is reported missing in that case.
//
// One without the other is different: the checkpoint is describing latent
// attention and half the description is unreadable. The absent half is reported
// missing so that a reader is told which key it would need, instead of falling
// through to the head-based layout and computing a number off the wrong shape.
//
// DeepSeek V4 is the exception, and it is recognised by architecture rather than
// by model: its configs state qk_rope_head_dim with no kv_lora_rank at all, and
// treating that as a half-described V3 layer reports a gap that does not exist.
// It is not the same layer. V4 states head_dim (512 in both released Pro and
// Flash checkpoints) and caches exactly that per KV head — the attention
// projection attn.wkv.weight has 512 output rows and attn.kv_norm.weight is
// 512 wide — so qk_rope_head_dim names a rotary slice inside head_dim, not a
// second width added on top of a latent rank. Adding the two the way V3 does
// would over-state V4's cache by 12.5%.
//
// The recognition keys on compress_ratios because that is the one key only this
// architecture writes, and because it is also what a reader needs in order to
// size the cache at all. Keying on architectures[0] would be worse in the same
// way it is everywhere else in this package: DeepseekV4ForCausalLM is one
// spelling of a family that will be re-spelled, while the layout key is the
// layout.
func applyLatentAttention(info *v1.ModelInfo, cfg *hfConfig) {
	if cfg.KVLoraRank == nil && (cfg.QKRopeHeadDim == nil || len(cfg.CompressRatios) > 0) {
		if cfg.QKRopeHeadDim != nil {
			info.QKRopeHeadDim = cfg.QKRopeHeadDim
			info.SetFieldSource(v1.ModelInfoFieldQKRopeHeadDim, v1.ModelInfoSourceAuto)
		}

		return
	}

	info.KVLoraRank = cfg.KVLoraRank
	recordAuto(info, v1.ModelInfoFieldKVLoraRank, cfg.KVLoraRank != nil)

	info.QKRopeHeadDim = cfg.QKRopeHeadDim
	recordAuto(info, v1.ModelInfoFieldQKRopeHeadDim, cfg.QKRopeHeadDim != nil)
}

// applyLayerTypes copies the per-layer attention kinds through untouched. The
// strings are not mapped onto any classification here: the set is open —
// "sliding_attention" and "full_attention" from one architecture, "mamba" and
// "attention" from another — and deciding what an unseen name means is the
// guessing this package refuses to do.
//
// A config that does not state layer_types leaves the field neither sourced nor
// missing. Its absence is not evidence of uniform layers: an older checkpoint
// states a hybrid layout through architecture-specific keys instead
// (sliding_window_pattern, full_attn_mod, no_rope_layers), and those are not
// read here because each is one architecture's private spelling.
func applyLayerTypes(info *v1.ModelInfo, cfg *hfConfig) {
	if len(cfg.LayerTypes) == 0 {
		return
	}

	info.LayerTypes = cfg.LayerTypes
	info.SetFieldSource(v1.ModelInfoFieldLayerTypes, v1.ModelInfoSourceAuto)
}

// applyExperts decides dense vs. MoE. Reading the config at all settles is_moe,
// so it is never missing. The expert counts only apply to an MoE checkpoint; on
// a dense one they are neither sourced nor missing, because there is nothing
// there to know.
func applyExperts(info *v1.ModelInfo, cfg *hfConfig) {
	experts := cfg.NumLocalExperts
	if experts == nil {
		experts = cfg.NumExperts
	}

	isMoE := experts != nil && *experts > 0
	info.IsMoE = &isMoE
	info.SetFieldSource(v1.ModelInfoFieldIsMoE, v1.ModelInfoSourceAuto)

	if !isMoE {
		return
	}

	info.NumExperts = experts
	info.SetFieldSource(v1.ModelInfoFieldNumExperts, v1.ModelInfoSourceAuto)

	info.NumExpertsPerToken = cfg.NumExpertsPerTok
	recordAuto(info, v1.ModelInfoFieldNumExpertsPerToken, cfg.NumExpertsPerTok != nil)
}

// applyQuantization reads the weight width. A checkpoint with no
// quantization_config is simply not quantized, which is an answer rather than a
// gap, so quantization_bits is left out of MissingFields in that case.
func applyQuantization(info *v1.ModelInfo, cfg *hfConfig) {
	if cfg.QuantizationConfig == nil {
		return
	}

	bits := cfg.QuantizationConfig.bits()
	info.QuantizationBits = bits
	recordAuto(info, v1.ModelInfoFieldQuantizationBits, bits != nil)
}

// applyParameterCount sums the shapes declared in the safetensors headers. This
// is an exact count, not an estimate: it reads the tens of kilobytes of header
// at the front of each shard and no weight data at all.
//
// Only safetensors is supported. A pytorch_model.bin is a pickle — parsing it in
// Go is impractical and executing it is a security hazard — and GGUF is a
// different container entirely; both leave parameter_count missing.
func applyParameterCount(info *v1.ModelInfo, dir string) {
	total, err := sumSafetensorsElements(dir)
	if err != nil || total == nil {
		info.MarkFieldMissing(v1.ModelInfoFieldParameterCount)

		return
	}

	// Reported as the exact decimal total so a caller can compare or reformat it.
	// The value counts every element stored in the checkpoint, which is what the
	// files actually contain — not strictly the trainable parameter count, since
	// a checkpoint holds a few non-parameter tensors such as rotary embedding
	// inverse frequencies. For an MoE checkpoint it is the total across all
	// experts, not the per-token active count.
	info.ParameterCount = strconv.FormatInt(*total, 10)
	info.SetFieldSource(v1.ModelInfoFieldParameterCount, v1.ModelInfoSourceAuto)
}

// recordAuto marks a field as read straight from the checkpoint, or as missing
// when the checkpoint did not state it. Everything this package establishes is
// auto except head_dim's fallback, which records its own provenance.
func recordAuto(info *v1.ModelInfo, field string, resolved bool) {
	if resolved {
		info.SetFieldSource(field, v1.ModelInfoSourceAuto)

		return
	}

	info.MarkFieldMissing(field)
}
