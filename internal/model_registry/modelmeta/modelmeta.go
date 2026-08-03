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
	v1.ModelInfoFieldMaxPositionEmbeddings,
	v1.ModelInfoFieldContextLength,
	v1.ModelInfoFieldParameterDtype,
	v1.ModelInfoFieldIsMoE,
	v1.ModelInfoFieldNumExperts,
	v1.ModelInfoFieldNumExpertsPerToken,
	v1.ModelInfoFieldQuantizationBits,
	v1.ModelInfoFieldParameterCount,
}

// hfConfig is the subset of config.json this package reads. Every scalar is a
// pointer so that a key present with value 0 is distinguishable from an absent
// key — the difference between "the checkpoint says zero" and "we don't know".
type hfConfig struct {
	Architectures         []string            `json:"architectures"`
	NumHiddenLayers       *int                `json:"num_hidden_layers"`
	NumAttentionHeads     *int                `json:"num_attention_heads"`
	NumKeyValueHeads      *int                `json:"num_key_value_heads"`
	HeadDim               *int                `json:"head_dim"`
	HiddenSize            *int                `json:"hidden_size"`
	MaxPositionEmbeddings *int                `json:"max_position_embeddings"`
	TorchDtype            string              `json:"torch_dtype"`
	NumLocalExperts       *int                `json:"num_local_experts"`
	NumExperts            *int                `json:"num_experts"`
	NumExpertsPerTok      *int                `json:"num_experts_per_tok"`
	QuantizationConfig    *quantizationConfig `json:"quantization_config"`
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
	info := &v1.ModelInfo{}

	cfg := readConfig(dir)
	if cfg == nil {
		info.MissingFields = append(info.MissingFields, allFields...)

		return info
	}

	applyConfig(info, cfg)
	applyParameterCount(info, dir)

	return info
}

func readConfig(dir string) *hfConfig {
	raw, err := os.ReadFile(filepath.Join(dir, ConfigFileName))
	if err != nil {
		return nil
	}

	var cfg hfConfig
	if err := json.Unmarshal(raw, &cfg); err != nil {
		return nil
	}

	return &cfg
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

	info.MaxPositionEmbeddings = cfg.MaxPositionEmbeddings
	recordAuto(info, v1.ModelInfoFieldMaxPositionEmbeddings, cfg.MaxPositionEmbeddings != nil)

	if cfg.MaxPositionEmbeddings != nil {
		info.ContextLength = strconv.Itoa(*cfg.MaxPositionEmbeddings)
	}

	recordAuto(info, v1.ModelInfoFieldContextLength, cfg.MaxPositionEmbeddings != nil)

	info.ParameterDtype = cfg.TorchDtype
	recordAuto(info, v1.ModelInfoFieldParameterDtype, cfg.TorchDtype != "")

	applyExperts(info, cfg)
	applyQuantization(info, cfg)
}

// applyHeadDim resolves the one arithmetic derivation this package allows:
// hidden_size / num_attention_heads when head_dim is absent. That is the
// established Hugging Face convention for the fallback, not a guess.
func applyHeadDim(info *v1.ModelInfo, cfg *hfConfig) {
	switch {
	case cfg.HeadDim != nil:
		info.HeadDim = cfg.HeadDim
		info.SetFieldSource(v1.ModelInfoFieldHeadDim, v1.ModelInfoSourceAuto)
	case cfg.HiddenSize != nil && cfg.NumAttentionHeads != nil && *cfg.NumAttentionHeads > 0:
		derived := *cfg.HiddenSize / *cfg.NumAttentionHeads
		info.HeadDim = &derived
		info.SetFieldSource(v1.ModelInfoFieldHeadDim, v1.ModelInfoSourceDerived)
	default:
		info.MarkFieldMissing(v1.ModelInfoFieldHeadDim)
	}
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
