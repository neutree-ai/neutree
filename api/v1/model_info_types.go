package v1

// Model info field provenance. Provenance is recorded per field rather than per
// record: the UI has to mark the individual values a user typed, and a resource
// estimator has to judge its confidence from the provenance of the specific
// parameters it reads.
const (
	// ModelInfoSourceAuto marks a value read straight out of the checkpoint.
	ModelInfoSourceAuto = "auto"
	// ModelInfoSourceDerived marks a value computed from other checkpoint
	// values by a documented convention rather than read directly.
	ModelInfoSourceDerived = "derived"
	// ModelInfoSourceManual marks a value supplied by a user.
	ModelInfoSourceManual = "manual"
)

// Keys used in ModelInfo.FieldSources and ModelInfo.MissingFields. They are the
// JSON names of the fields they describe, so a client can index the maps with
// the same key it reads the value under.
const (
	ModelInfoFieldParameterCount        = "parameter_count"
	ModelInfoFieldQuantization          = "quantization"
	ModelInfoFieldContextLength         = "context_length"
	ModelInfoFieldArchitecture          = "architecture"
	ModelInfoFieldNumHiddenLayers       = "num_hidden_layers"
	ModelInfoFieldNumAttentionHeads     = "num_attention_heads"
	ModelInfoFieldNumKeyValueHeads      = "num_key_value_heads"
	ModelInfoFieldHeadDim               = "head_dim"
	ModelInfoFieldKVLoraRank            = "kv_lora_rank"
	ModelInfoFieldQKRopeHeadDim         = "qk_rope_head_dim"
	ModelInfoFieldLayerTypes            = "layer_types"
	ModelInfoFieldSlidingWindow         = "sliding_window"
	ModelInfoFieldMaxPositionEmbeddings = "max_position_embeddings"
	ModelInfoFieldIsMoE                 = "is_moe"
	ModelInfoFieldNumExperts            = "num_experts"
	ModelInfoFieldParameterDtype        = "parameter_dtype"
	ModelInfoFieldQuantizationBits      = "quantization_bits"
	// "token" here is a unit of text, not a credential — the secret scanner keys
	// off the name.
	ModelInfoFieldNumExpertsPerToken = "num_experts_per_token" //nolint:gosec

	// Widths of a linear-attention layer's recurrent state.
	ModelInfoFieldLinearConvKernelDim = "linear_conv_kernel_dim"
	ModelInfoFieldLinearNumKeyHeads   = "linear_num_key_heads"
	ModelInfoFieldLinearKeyHeadDim    = "linear_key_head_dim"
	ModelInfoFieldLinearNumValueHeads = "linear_num_value_heads"
	ModelInfoFieldLinearValueHeadDim  = "linear_value_head_dim"
	ModelInfoFieldRecurrentStateDtype = "recurrent_state_dtype"

	// DeepSeek V4 style sparse attention.
	ModelInfoFieldCompressRatios = "compress_ratios"
	ModelInfoFieldIndexNumHeads  = "index_n_heads"
	ModelInfoFieldIndexHeadDim   = "index_head_dim"
	ModelInfoFieldIndexTopK      = "index_topk"

	ModelInfoFieldMTPNumLayers = "mtp_num_layers"
)

// ModelInfo is metadata describing the model checkpoint a variant points at. It
// belongs to the model (not the catalog template), so it lives on ModelSpec and
// is reused wherever a model is referenced.
//
// Two populations meet in this struct and they are not equally trustworthy: the
// four display fields at the top are typed by hand into a catalog, while the
// structured fields below them are read out of the checkpoint. FieldSources is
// what tells them apart — consult it before presenting a value as a fact about
// the checkpoint.
//
// Every field is optional and omitted when unset, so a spec that carries none of
// them serializes exactly as it did before the structured fields existed.
type ModelInfo struct {
	// ParameterCount is the model's parameter count. A checkpoint-derived value
	// is the exact total element count of the stored tensors, written as a plain
	// decimal string; a hand-written catalog value is free-form (e.g. "72.7B").
	ParameterCount string `json:"parameter_count,omitempty"`
	// Quantization is a display label (e.g. "bf16" / "fp8"). It is never derived
	// from a checkpoint — QuantizationBits is the parsed counterpart.
	Quantization string `json:"quantization,omitempty"`
	// ContextLength is the usable context window as a string (e.g. "32K" by hand,
	// or the token count when read from the checkpoint).
	ContextLength string `json:"context_length,omitempty"`
	// Architecture is the model architecture (e.g. "dense" / "moe" by hand, or
	// the checkpoint's architectures[0] such as "Qwen2ForCausalLM").
	Architecture string `json:"architecture,omitempty"`

	// The fields below are the structured shape of the checkpoint. They are
	// pointers so that a legitimate zero stays distinguishable from "unknown";
	// an unknown field is absent here and named in MissingFields.
	NumHiddenLayers   *int `json:"num_hidden_layers,omitempty"`
	NumAttentionHeads *int `json:"num_attention_heads,omitempty"`
	NumKeyValueHeads  *int `json:"num_key_value_heads,omitempty"`
	HeadDim           *int `json:"head_dim,omitempty"`

	// KVLoraRank and QKRopeHeadDim describe an MLA layer, whose cache holds one
	// compressed latent of width kv_lora_rank plus one decoupled RoPE key of
	// width qk_rope_head_dim per token — not a key and a value per KV head.
	//
	// Their presence is the only thing that tells the two layouts apart. An MLA
	// checkpoint also states num_key_value_heads and head_dim, describing the
	// heads attention is computed over rather than what is cached, so a reader
	// that goes by those alone reports a plausible number that is wrong by more
	// than an order of magnitude.
	KVLoraRank    *int `json:"kv_lora_rank,omitempty"`
	QKRopeHeadDim *int `json:"qk_rope_head_dim,omitempty"`

	// LayerTypes is the per-layer attention kind, verbatim as the checkpoint
	// states it ("full_attention", "sliding_attention", "mamba", …). The
	// vocabulary is open and grows with every hybrid architecture, so the value
	// is reported without interpretation.
	//
	// What it establishes without knowing the vocabulary is whether the layers
	// are all alike, which is the precondition for describing the model by one
	// layer times the layer count. A checkpoint that does not state it declares
	// nothing about its layers being heterogeneous.
	LayerTypes []string `json:"layer_types,omitempty"`

	// SlidingWindow is the attention window of the layers that use one, in
	// tokens. Those layers cache at most this many tokens however long the
	// sequence gets, which is the whole reason it is worth reporting.
	//
	// It is only set when the same checkpoint also says which layers the window
	// applies to — see modelmeta for the criterion. A window nobody can place is
	// not a fact about the cache.
	SlidingWindow *int `json:"sliding_window,omitempty"`

	// The widths of one linear-attention layer's recurrent state, as the
	// Qwen3.5-generation hybrids state them. Such a layer caches a fixed-size
	// state per sequence instead of a per-token key and value, so its cost does
	// not grow with the sequence and none of the head fields describe it.
	//
	// The state is two parts: a short-convolution history of
	// linear_conv_kernel_dim - 1 steps over
	// 2*linear_num_key_heads*linear_key_head_dim + linear_num_value_heads*linear_value_head_dim
	// channels, and a per-head matrix of
	// linear_num_value_heads x linear_key_head_dim x linear_value_head_dim.
	LinearConvKernelDim *int `json:"linear_conv_kernel_dim,omitempty"`
	LinearNumKeyHeads   *int `json:"linear_num_key_heads,omitempty"`
	LinearKeyHeadDim    *int `json:"linear_key_head_dim,omitempty"`
	LinearNumValueHeads *int `json:"linear_num_value_heads,omitempty"`
	LinearValueHeadDim  *int `json:"linear_value_head_dim,omitempty"`
	// RecurrentStateDtype is the dtype the recurrent state above is held in,
	// which is routinely wider than the weights — float32 state on a bfloat16
	// checkpoint. Read from the checkpoint's mamba_ssm_dtype.
	RecurrentStateDtype string `json:"recurrent_state_dtype,omitempty"`

	// CompressRatios is DeepSeek V4's per-layer sparse-attention schedule: one
	// entry per layer, in layer order, stating how many tokens that layer folds
	// into one cached slot. Zero means the layer compresses nothing and keeps
	// only its sliding window.
	//
	// It can be longer than num_hidden_layers. The surplus entries describe the
	// checkpoint's draft (MTP) modules, one entry each, and are the only place
	// the checkpoint says how many of those it holds — num_nextn_predict_layers
	// counts the layers inside one module, not the modules. Both are reported;
	// the surplus length is what a reader needs for draft KV.
	CompressRatios []int `json:"compress_ratios,omitempty"`
	// IndexNumHeads, IndexHeadDim and IndexTopK describe the sparse-attention
	// indexer that selects which cached tokens a layer attends to. Only
	// IndexHeadDim is a cache width; the other two size the query side and the
	// selection budget and are reported because they identify the layout, not
	// because they enter a byte count.
	IndexNumHeads *int `json:"index_n_heads,omitempty"`
	IndexHeadDim  *int `json:"index_head_dim,omitempty"`
	IndexTopK     *int `json:"index_topk,omitempty"`

	// MTPNumLayers is how many transformer layers one multi-token-prediction
	// (draft) module contains — spelled num_nextn_predict_layers by DeepSeek and
	// mtp_num_hidden_layers by Qwen.
	//
	// It is deliberately not called a module count. Neither spelling states one:
	// DeepSeek-V4-Pro-0813 says 1 here while carrying three draft modules. A
	// reader that needs the module count has to get it elsewhere, which for a V4
	// checkpoint is the surplus length of CompressRatios.
	MTPNumLayers *int `json:"mtp_num_layers,omitempty"`

	MaxPositionEmbeddings *int   `json:"max_position_embeddings,omitempty"`
	IsMoE                 *bool  `json:"is_moe,omitempty"`
	NumExperts            *int   `json:"num_experts,omitempty"`
	NumExpertsPerToken    *int   `json:"num_experts_per_token,omitempty"`
	ParameterDtype        string `json:"parameter_dtype,omitempty"`
	QuantizationBits      *int   `json:"quantization_bits,omitempty"`

	// FieldSources maps a field key to how that field's value was established:
	// ModelInfoSourceAuto, ModelInfoSourceDerived or ModelInfoSourceManual. A
	// field carrying a value but no entry here has unknown provenance — a legacy
	// catalog, typically.
	FieldSources map[string]string `json:"field_sources,omitempty"`
	// MissingFields names the fields that were looked for and not established.
	// It is a convenience: it is exactly the set of known fields that hold no
	// value and have no FieldSources entry. Fields that do not apply to the model
	// at all (expert counts on a dense model, quantization bits on an unquantized
	// one) are not listed.
	MissingFields []string `json:"missing_fields,omitempty"`
}

// SetFieldSource records where field's value came from.
func (i *ModelInfo) SetFieldSource(field, source string) {
	if i.FieldSources == nil {
		i.FieldSources = make(map[string]string)
	}

	i.FieldSources[field] = source
}

// MarkFieldMissing appends field to MissingFields unless it is already named
// there, and drops any stale provenance for it.
func (i *ModelInfo) MarkFieldMissing(field string) {
	delete(i.FieldSources, field)

	for _, existing := range i.MissingFields {
		if existing == field {
			return
		}
	}

	i.MissingFields = append(i.MissingFields, field)
}

// MergeManual overlays user-supplied values on top of the receiver, marking
// every field it takes as ModelInfoSourceManual and retracting it from
// MissingFields.
//
// A user's value wins over a parsed one. Hand-filling a field is normally how a
// gap gets closed, but when someone contradicts the checkpoint it is because
// they know something the files do not say, and silently keeping the parsed
// value would make the correction invisible.
func (i *ModelInfo) MergeManual(manual *ModelInfo) {
	if manual == nil {
		return
	}

	i.mergeManualStrings(manual)
	i.mergeManualNumbers(manual)

	if manual.IsMoE != nil {
		i.IsMoE = manual.IsMoE
		i.takeManual(ModelInfoFieldIsMoE)
	}

	if len(manual.LayerTypes) > 0 {
		i.LayerTypes = manual.LayerTypes
		i.takeManual(ModelInfoFieldLayerTypes)
	}

	if len(manual.CompressRatios) > 0 {
		i.CompressRatios = manual.CompressRatios
		i.takeManual(ModelInfoFieldCompressRatios)
	}
}

func (i *ModelInfo) mergeManualStrings(manual *ModelInfo) {
	values := []struct {
		field string
		value string
		into  *string
	}{
		{ModelInfoFieldParameterCount, manual.ParameterCount, &i.ParameterCount},
		{ModelInfoFieldQuantization, manual.Quantization, &i.Quantization},
		{ModelInfoFieldContextLength, manual.ContextLength, &i.ContextLength},
		{ModelInfoFieldArchitecture, manual.Architecture, &i.Architecture},
		{ModelInfoFieldParameterDtype, manual.ParameterDtype, &i.ParameterDtype},
		{ModelInfoFieldRecurrentStateDtype, manual.RecurrentStateDtype, &i.RecurrentStateDtype},
	}

	for _, entry := range values {
		if entry.value == "" {
			continue
		}

		*entry.into = entry.value

		i.takeManual(entry.field)
	}
}

func (i *ModelInfo) mergeManualNumbers(manual *ModelInfo) {
	numbers := []struct {
		field string
		value *int
		into  **int
	}{
		{ModelInfoFieldNumHiddenLayers, manual.NumHiddenLayers, &i.NumHiddenLayers},
		{ModelInfoFieldNumAttentionHeads, manual.NumAttentionHeads, &i.NumAttentionHeads},
		{ModelInfoFieldNumKeyValueHeads, manual.NumKeyValueHeads, &i.NumKeyValueHeads},
		{ModelInfoFieldHeadDim, manual.HeadDim, &i.HeadDim},
		{ModelInfoFieldKVLoraRank, manual.KVLoraRank, &i.KVLoraRank},
		{ModelInfoFieldQKRopeHeadDim, manual.QKRopeHeadDim, &i.QKRopeHeadDim},
		{ModelInfoFieldSlidingWindow, manual.SlidingWindow, &i.SlidingWindow},
		{ModelInfoFieldLinearConvKernelDim, manual.LinearConvKernelDim, &i.LinearConvKernelDim},
		{ModelInfoFieldLinearNumKeyHeads, manual.LinearNumKeyHeads, &i.LinearNumKeyHeads},
		{ModelInfoFieldLinearKeyHeadDim, manual.LinearKeyHeadDim, &i.LinearKeyHeadDim},
		{ModelInfoFieldLinearNumValueHeads, manual.LinearNumValueHeads, &i.LinearNumValueHeads},
		{ModelInfoFieldLinearValueHeadDim, manual.LinearValueHeadDim, &i.LinearValueHeadDim},
		{ModelInfoFieldIndexNumHeads, manual.IndexNumHeads, &i.IndexNumHeads},
		{ModelInfoFieldIndexHeadDim, manual.IndexHeadDim, &i.IndexHeadDim},
		{ModelInfoFieldIndexTopK, manual.IndexTopK, &i.IndexTopK},
		{ModelInfoFieldMTPNumLayers, manual.MTPNumLayers, &i.MTPNumLayers},
		{ModelInfoFieldMaxPositionEmbeddings, manual.MaxPositionEmbeddings, &i.MaxPositionEmbeddings},
		{ModelInfoFieldNumExperts, manual.NumExperts, &i.NumExperts},
		{ModelInfoFieldNumExpertsPerToken, manual.NumExpertsPerToken, &i.NumExpertsPerToken},
		{ModelInfoFieldQuantizationBits, manual.QuantizationBits, &i.QuantizationBits},
	}

	for _, entry := range numbers {
		if entry.value == nil {
			continue
		}

		*entry.into = entry.value

		i.takeManual(entry.field)
	}
}

func (i *ModelInfo) takeManual(field string) {
	i.ClearFieldMissing(field)
	i.SetFieldSource(field, ModelInfoSourceManual)
}

// ClearFieldMissing removes field from MissingFields, for when a later pass
// establishes a value the earlier one could not.
func (i *ModelInfo) ClearFieldMissing(field string) {
	remaining := i.MissingFields[:0]

	for _, existing := range i.MissingFields {
		if existing != field {
			remaining = append(remaining, existing)
		}
	}

	if len(remaining) == 0 {
		i.MissingFields = nil

		return
	}

	i.MissingFields = remaining
}
