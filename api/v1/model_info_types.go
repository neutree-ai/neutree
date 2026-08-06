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
	ModelInfoFieldMaxPositionEmbeddings = "max_position_embeddings"
	ModelInfoFieldIsMoE                 = "is_moe"
	ModelInfoFieldNumExperts            = "num_experts"
	ModelInfoFieldParameterDtype        = "parameter_dtype"
	ModelInfoFieldQuantizationBits      = "quantization_bits"
	// "token" here is a unit of text, not a credential — the secret scanner keys
	// off the name.
	ModelInfoFieldNumExpertsPerToken = "num_experts_per_token" //nolint:gosec
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
	NumHiddenLayers       *int   `json:"num_hidden_layers,omitempty"`
	NumAttentionHeads     *int   `json:"num_attention_heads,omitempty"`
	NumKeyValueHeads      *int   `json:"num_key_value_heads,omitempty"`
	HeadDim               *int   `json:"head_dim,omitempty"`
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
