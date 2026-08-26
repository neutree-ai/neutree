package model

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

// infoField binds a ModelInfo field's API name to the way its value is read.
// The names are the constants the API declares, so a field renamed there stops
// compiling here instead of quietly drifting out of step with the payload.
type infoField struct {
	name string
	read func(*v1.ModelInfo) string
}

// infoFields lists the checkpoint metadata in the order ModelInfo declares it:
// the four display fields a catalog can carry by hand, then the structured shape
// read out of the checkpoint.
var infoFields = []infoField{
	{v1.ModelInfoFieldParameterCount, func(i *v1.ModelInfo) string { return i.ParameterCount }},
	{v1.ModelInfoFieldQuantization, func(i *v1.ModelInfo) string { return i.Quantization }},
	{v1.ModelInfoFieldContextLength, func(i *v1.ModelInfo) string { return i.ContextLength }},
	{v1.ModelInfoFieldArchitecture, func(i *v1.ModelInfo) string { return i.Architecture }},
	{v1.ModelInfoFieldNumHiddenLayers, func(i *v1.ModelInfo) string { return formatInt(i.NumHiddenLayers) }},
	{v1.ModelInfoFieldNumAttentionHeads, func(i *v1.ModelInfo) string { return formatInt(i.NumAttentionHeads) }},
	{v1.ModelInfoFieldNumKeyValueHeads, func(i *v1.ModelInfo) string { return formatInt(i.NumKeyValueHeads) }},
	{v1.ModelInfoFieldHeadDim, func(i *v1.ModelInfo) string { return formatInt(i.HeadDim) }},
	{v1.ModelInfoFieldKVLoraRank, func(i *v1.ModelInfo) string { return formatInt(i.KVLoraRank) }},
	{v1.ModelInfoFieldQKRopeHeadDim, func(i *v1.ModelInfo) string { return formatInt(i.QKRopeHeadDim) }},
	{v1.ModelInfoFieldLayerTypes, func(i *v1.ModelInfo) string { return formatLayerTypes(i.LayerTypes) }},
	{v1.ModelInfoFieldSlidingWindow, func(i *v1.ModelInfo) string { return formatInt(i.SlidingWindow) }},
	{v1.ModelInfoFieldLinearConvKernelDim, func(i *v1.ModelInfo) string { return formatInt(i.LinearConvKernelDim) }},
	{v1.ModelInfoFieldLinearNumKeyHeads, func(i *v1.ModelInfo) string { return formatInt(i.LinearNumKeyHeads) }},
	{v1.ModelInfoFieldLinearKeyHeadDim, func(i *v1.ModelInfo) string { return formatInt(i.LinearKeyHeadDim) }},
	{v1.ModelInfoFieldLinearNumValueHeads, func(i *v1.ModelInfo) string { return formatInt(i.LinearNumValueHeads) }},
	{v1.ModelInfoFieldLinearValueHeadDim, func(i *v1.ModelInfo) string { return formatInt(i.LinearValueHeadDim) }},
	{v1.ModelInfoFieldRecurrentStateDtype, func(i *v1.ModelInfo) string { return i.RecurrentStateDtype }},
	{v1.ModelInfoFieldCompressRatios, func(i *v1.ModelInfo) string { return formatCompressRatios(i.CompressRatios) }},
	{v1.ModelInfoFieldIndexNumHeads, func(i *v1.ModelInfo) string { return formatInt(i.IndexNumHeads) }},
	{v1.ModelInfoFieldIndexHeadDim, func(i *v1.ModelInfo) string { return formatInt(i.IndexHeadDim) }},
	{v1.ModelInfoFieldIndexTopK, func(i *v1.ModelInfo) string { return formatInt(i.IndexTopK) }},
	{v1.ModelInfoFieldMTPNumLayers, func(i *v1.ModelInfo) string { return formatInt(i.MTPNumLayers) }},
	{v1.ModelInfoFieldMaxPositionEmbeddings, func(i *v1.ModelInfo) string { return formatInt(i.MaxPositionEmbeddings) }},
	{v1.ModelInfoFieldIsMoE, func(i *v1.ModelInfo) string { return formatBool(i.IsMoE) }},
	{v1.ModelInfoFieldNumExperts, func(i *v1.ModelInfo) string { return formatInt(i.NumExperts) }},
	{v1.ModelInfoFieldNumExpertsPerToken, func(i *v1.ModelInfo) string { return formatInt(i.NumExpertsPerToken) }},
	{v1.ModelInfoFieldParameterDtype, func(i *v1.ModelInfo) string { return i.ParameterDtype }},
	{v1.ModelInfoFieldQuantizationBits, func(i *v1.ModelInfo) string { return formatInt(i.QuantizationBits) }},
}

type getOptions struct {
	output string
}

func NewGetCmd() *cobra.Command {
	opts := &getOptions{}

	cmd := &cobra.Command{
		Use:          "get [model_name:version]",
		Short:        "Get detailed information about a model",
		Long:         `Get detailed information about a specific model in the registry`,
		Args:         cobra.ExactArgs(1),
		SilenceUsage: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(opts, args[0])
		},
	}

	addOutputFlag(cmd, &opts.output)

	return cmd
}

func runGet(opts *getOptions, modelTag string) error {
	// Parse model tag
	modelName, version, err := client.ParseModelTag(modelTag)
	if err != nil {
		return err
	}

	clientOptions := []client.ClientOption{
		client.WithAPIKey(global.APIKey),
	}

	if global.Insecure {
		clientOptions = append(clientOptions, client.WithInsecureSkipVerify())
	}

	// Create client
	c := client.NewClient(global.ServerURL, clientOptions...)

	_, err = c.ModelRegistries.Get(workspace, registry) // Ensure registry exists
	if err != nil {
		return fmt.Errorf("failed to get model registry %s: %w", registry, err)
	}

	// Get model details
	detail, err := c.Models.Get(workspace, registry, modelName, version)
	if err != nil {
		return fmt.Errorf("failed to get model %s in registry %q: %w", modelTag, registry, err)
	}

	printed, err := printPayload(os.Stdout, opts.output, detail.Raw)
	if err != nil || printed {
		return err
	}

	return renderModelDetail(os.Stdout, modelName, detail.Version)
}

// renderModelDetail writes one model version as label/value lines.
//
// Labels are the API's own names for what they show. Two of them come from the
// request rather than the body: the body is a version, so its "name" field is
// the version and it carries no model name at all — the two are labelled with
// the path and query parameters that selected them, "model" and "version".
func renderModelDetail(out io.Writer, modelName string, modelVersion *v1.ModelVersion) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	fmt.Fprintf(w, "model:\t%s\n", modelName)
	fmt.Fprintf(w, "version:\t%s\n", orUnknown(modelVersion.Name))
	// An unset alias is not a gap in what the registry knows: this version was
	// never given a display name, and it still answers to its physical name.
	fmt.Fprintf(w, "alias:\t%s\n", orUnset(modelVersion.Alias))
	fmt.Fprintf(w, "size:\t%s\n", orUnknown(modelVersion.Size))
	fmt.Fprintf(w, "creation_time:\t%s\n", orUnknown(modelVersion.CreationTime))

	if modelVersion.Module != "" {
		fmt.Fprintf(w, "module:\t%s\n", modelVersion.Module)
	}

	if modelVersion.Description != "" {
		fmt.Fprintf(w, "description:\t%s\n", modelVersion.Description)
	}

	if len(modelVersion.Labels) > 0 {
		fmt.Fprintln(w, "labels:")

		keys := make([]string, 0, len(modelVersion.Labels))
		for k := range modelVersion.Labels {
			keys = append(keys, k)
		}

		// Sorted, because ranging a map would reorder the block on every call.
		sort.Strings(keys)

		for _, k := range keys {
			fmt.Fprintf(w, "  %s:\t%s\n", k, modelVersion.Labels[k])
		}
	}

	writeModelInfo(w, modelVersion.Info)

	return w.Flush()
}

// writeModelInfo renders what the checkpoint states about itself.
//
// Every value is shown with where it came from, because the values do not carry
// equal weight: one read out of the checkpoint is a fact about the files, one
// derived is a documented convention applied to those facts, and one a person
// typed is a claim about them. A field the registry looked for and could not
// establish — one named in missing_fields — is shown as unknown rather than left
// blank, so that a gap in the metadata cannot be misread as a rendering gap.
// A field that is absent without being named there does not apply to this model
// (expert counts on a dense model, say) and is not shown at all.
func writeModelInfo(w io.Writer, info *v1.ModelInfo) {
	if info == nil {
		return
	}

	fmt.Fprintln(w, "info:")

	missing := make(map[string]bool, len(info.MissingFields))
	for _, field := range info.MissingFields {
		missing[field] = true
	}

	for _, field := range infoFields {
		value := field.read(info)

		switch {
		case value != "":
			fmt.Fprintf(w, "  %s:\t%s%s\n", field.name, value, sourceSuffix(info.FieldSources[field.name]))
		case missing[field.name]:
			fmt.Fprintf(w, "  %s:\t%s\n", field.name, unknownValue)
		}

		delete(missing, field.name)
	}

	// A server newer than this build can report a field this build has no column
	// for. Naming it is more useful than dropping it: the reader learns the
	// registry looked and came up empty, which is the whole point of the list.
	remaining := make([]string, 0, len(missing))
	for field := range missing {
		remaining = append(remaining, field)
	}

	sort.Strings(remaining)

	for _, field := range remaining {
		fmt.Fprintf(w, "  %s:\t%s\n", field, unknownValue)
	}
}

// sourceSuffix annotates a value with its provenance. A value with no recorded
// provenance — a catalog written before provenance was tracked — is left
// unannotated rather than labelled unknown, which would read as a missing value.
func sourceSuffix(source string) string {
	if source == "" {
		return ""
	}

	return "  (" + source + ")"
}

func formatInt(value *int) string {
	if value == nil {
		return ""
	}

	return strconv.Itoa(*value)
}

// formatLayerTypes renders the per-layer attention kinds as a count per kind,
// in the order the kinds first appear. A model states one entry per layer, which
// is dozens of repeated names on one terminal line; the counts carry the same
// information — which kinds occur and how often — in a line a reader can take
// in. Rendered as "full_attention x12, sliding_attention x12".
func formatLayerTypes(types []string) string {
	var (
		order  []string
		counts = map[string]int{}
	)

	for _, layer := range types {
		if counts[layer] == 0 {
			order = append(order, layer)
		}

		counts[layer]++
	}

	parts := make([]string, 0, len(order))
	for _, layer := range order {
		parts = append(parts, fmt.Sprintf("%s x%d", layer, counts[layer]))
	}

	return strings.Join(parts, ", ")
}

// formatCompressRatios renders the per-layer compression schedule the same way
// formatLayerTypes renders layer kinds, and for the same reason: it holds one
// entry per layer, which is dozens of repeated numbers on one terminal line. The
// counts are shown per distinct rate, in the order the rates first appear, with
// the total length last because that length is itself a fact — a schedule longer
// than num_hidden_layers is how the checkpoint states its draft modules.
// Rendered as "128 x31, 4 x30, 0 x3 (64 entries)".
func formatCompressRatios(ratios []int) string {
	if len(ratios) == 0 {
		return ""
	}

	var (
		order  []int
		counts = map[int]int{}
	)

	for _, ratio := range ratios {
		if counts[ratio] == 0 {
			order = append(order, ratio)
		}

		counts[ratio]++
	}

	parts := make([]string, 0, len(order))
	for _, ratio := range order {
		parts = append(parts, fmt.Sprintf("%d x%d", ratio, counts[ratio]))
	}

	return fmt.Sprintf("%s (%d entries)", strings.Join(parts, ", "), len(ratios))
}

func formatBool(value *bool) string {
	if value == nil {
		return ""
	}

	return strconv.FormatBool(*value)
}
