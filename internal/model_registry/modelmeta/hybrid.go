package modelmeta

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
)

// This file reads the keys that describe a layer whose cache is not one key and
// one value per KV head per token. Three layouts are covered, and each one is
// recognised by the keys the checkpoint states rather than by its architecture
// string, for the reason the package doc gives: the architecture name is one
// spelling of a family, while the shape keys are the shape.
//
//   sliding attention   caps a layer's cache at a fixed window of tokens
//   linear attention    replaces it with a fixed-size recurrent state
//   sparse attention    keeps a window plus a compressed, per-layer-rate cache
//
// A checkpoint that states none of them is simply not one of these, which is an
// answer rather than a gap, so nothing is reported missing in that case. The
// converse is what these functions are careful about: once a checkpoint says it
// has such layers, a width it then fails to state is a real gap and is named, so
// that a reader is told which key it would have needed instead of falling
// through to a formula built for a different layer.

// slidingLayerType is the layer_types entry that says a layer runs windowed
// attention. transformers writes this one spelling across the architectures that
// declare their layout that way (Gemma, gpt-oss, Ministral, Cohere), and it is
// matched exactly rather than by prefix so that an unseen name stays unseen
// rather than being swept in by a substring.
const slidingLayerType = "sliding_attention"

// linearLayerType is the layer_types entry for a recurrent linear-attention
// layer, as the Qwen3.5-generation hybrids write it. It is matched exactly for
// the same reason: "mamba" and "mamba2" also name recurrent layers and do not
// have the same state shape, so answering for them off these keys would be a
// guess.
const linearLayerType = "linear_attention"

// applySlidingWindow reports the attention window, but only when the same config
// also establishes that the window is in force and which layers it applies to.
//
// A bare sliding_window key is not that evidence, which is why it was left
// unparsed until now. Qwen2 and Mistral checkpoints carry sliding_window
// alongside use_sliding_window: false and transformers ignores it there, so a
// parser that reported the key alone would put a window on a model that caches
// every token — the same confident-wrong-number failure this package exists to
// avoid, in the direction that under-states the cache.
//
// The two statements accepted as corroboration are:
//
//   - layer_types naming at least one sliding layer. This says exactly which
//     layers are windowed, which is what a cache estimate needs anyway.
//   - compress_ratios, DeepSeek V4's sparse-attention schedule. Every layer of
//     that architecture keeps a window of sliding_window tokens, including the
//     ratio-zero layers that keep nothing else, so the schedule's presence
//     places the window on all of them.
//
// use_sliding_window: false overrides both. It is the checkpoint stating in its
// own words that the window is off, and no inference outranks that.
func applySlidingWindow(info *v1.ModelInfo, cfg *hfConfig) {
	if cfg.UseSlidingWindow != nil && !*cfg.UseSlidingWindow {
		return
	}

	placed := len(cfg.CompressRatios) > 0 || hasLayerType(cfg.LayerTypes, slidingLayerType)
	if !placed {
		return
	}

	if cfg.SlidingWindow == nil {
		info.MarkFieldMissing(v1.ModelInfoFieldSlidingWindow)

		return
	}

	info.SlidingWindow = cfg.SlidingWindow
	info.SetFieldSource(v1.ModelInfoFieldSlidingWindow, v1.ModelInfoSourceAuto)
}

// applyLinearAttention reports the widths of one linear-attention layer's
// recurrent state.
//
// Such a layer caches a fixed-size state per sequence rather than a per-token
// key and value, so nothing about it scales with the sequence and none of the
// head fields describe it. A reader that saw only num_key_value_heads and
// head_dim on a Qwen3.5-generation hybrid would apply the head formula to all 64
// layers when only 16 of them cache per token.
//
// The five widths are reported as a set: a checkpoint that states any of them is
// describing this layout, and any it then omits is named, because the state size
// is a product of all five and no one of them can be defaulted.
func applyLinearAttention(info *v1.ModelInfo, cfg *hfConfig) {
	widths := []struct {
		field string
		value *int
		into  **int
	}{
		{v1.ModelInfoFieldLinearConvKernelDim, cfg.LinearConvKernelDim, &info.LinearConvKernelDim},
		{v1.ModelInfoFieldLinearNumKeyHeads, cfg.LinearNumKeyHeads, &info.LinearNumKeyHeads},
		{v1.ModelInfoFieldLinearKeyHeadDim, cfg.LinearKeyHeadDim, &info.LinearKeyHeadDim},
		{v1.ModelInfoFieldLinearNumValueHeads, cfg.LinearNumValueHeads, &info.LinearNumValueHeads},
		{v1.ModelInfoFieldLinearValueHeadDim, cfg.LinearValueHeadDim, &info.LinearValueHeadDim},
	}

	stated := hasLayerType(cfg.LayerTypes, linearLayerType)

	for _, width := range widths {
		if width.value != nil {
			stated = true
		}
	}

	if !stated {
		return
	}

	for _, width := range widths {
		*width.into = width.value
		recordAuto(info, width.field, width.value != nil)
	}

	// The state is routinely held wider than the weights — float32 state on a
	// bfloat16 checkpoint — so the weight dtype does not answer for it. Absent,
	// it is a gap like any other width: it multiplies the byte count.
	info.RecurrentStateDtype = cfg.MambaSSMDtype
	recordAuto(info, v1.ModelInfoFieldRecurrentStateDtype, cfg.MambaSSMDtype != "")
}

// applySparseAttention reports DeepSeek V4's per-layer compression schedule and
// the widths of the indexer that goes with it.
//
// compress_ratios is passed through untouched, one entry per layer in layer
// order, because the interpretation of an individual value belongs to whoever
// computes with it and the array is the only thing that places the rates on
// layers. What it means is fixed by the weights: in both released V4
// checkpoints, and for every one of their 104 layers, a layer whose entry is 0
// carries neither attn.compressor.* nor attn.indexer.*, a layer whose entry is
// 128 carries the compressor only, and a layer whose entry is 4 carries both.
//
// The array can be longer than num_hidden_layers. Those surplus entries are the
// draft modules — DeepSeek-V4-Pro-0813 has three surplus entries and exactly
// three mtp.N modules in its weight index, each of them a single block with
// neither compressor nor indexer, matching their entry of 0 — and they are left
// in place rather than trimmed, because that surplus length is the only
// statement the checkpoint makes about how many draft modules it holds.
//
// index_head_dim is required once any layer indexes, since it is the width of
// what that layer caches for the indexer. index_n_heads and index_topk are
// reported when stated and never reported missing: they size the query side and
// the selection budget, so a reader that lacks them still gets a correct byte
// count.
func applySparseAttention(info *v1.ModelInfo, cfg *hfConfig) {
	if len(cfg.CompressRatios) == 0 {
		applyIndexerWidths(info, cfg, false)

		return
	}

	info.CompressRatios = cfg.CompressRatios
	info.SetFieldSource(v1.ModelInfoFieldCompressRatios, v1.ModelInfoSourceAuto)

	applyIndexerWidths(info, cfg, true)
}

func applyIndexerWidths(info *v1.ModelInfo, cfg *hfConfig, required bool) {
	stated := cfg.IndexNumHeads != nil || cfg.IndexHeadDim != nil || cfg.IndexTopK != nil
	if !stated && !required {
		return
	}

	info.IndexNumHeads = cfg.IndexNumHeads
	if cfg.IndexNumHeads != nil {
		info.SetFieldSource(v1.ModelInfoFieldIndexNumHeads, v1.ModelInfoSourceAuto)
	}

	info.IndexTopK = cfg.IndexTopK
	if cfg.IndexTopK != nil {
		info.SetFieldSource(v1.ModelInfoFieldIndexTopK, v1.ModelInfoSourceAuto)
	}

	info.IndexHeadDim = cfg.IndexHeadDim
	recordAuto(info, v1.ModelInfoFieldIndexHeadDim, cfg.IndexHeadDim != nil)
}

// applyMTP reports how many transformer layers one multi-token-prediction module
// holds, from either of the two spellings in use: DeepSeek writes
// num_nextn_predict_layers, Qwen writes mtp_num_hidden_layers.
//
// The two are read into one field because they state the same quantity, and that
// quantity is per module. It is not a module count and is not reported as one:
// DeepSeek-V4-Pro-0813 states 1 here while its weight index holds three mtp.N
// modules, each a single block. Qwen3.6-27B states 1 and holds one. Nothing in
// either config resolves the difference, so the field says only what both
// spellings actually mean and a reader needing the module count takes it from
// compress_ratios.
//
// A checkpoint stating neither spelling has no draft module to report, which is
// an answer rather than a gap.
func applyMTP(info *v1.ModelInfo, cfg *hfConfig) {
	layers := cfg.NumNextNPredictLayers
	if layers == nil {
		layers = cfg.MTPNumHiddenLayers
	}

	if layers == nil {
		return
	}

	info.MTPNumLayers = layers
	info.SetFieldSource(v1.ModelInfoFieldMTPNumLayers, v1.ModelInfoSourceAuto)
}

func hasLayerType(types []string, want string) bool {
	for _, layer := range types {
		if layer == want {
			return true
		}
	}

	return false
}
