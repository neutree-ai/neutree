package model

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strconv"
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
		Use:   "get [model_name:version]",
		Short: "Get detailed information about a model",
		Long:  `Get detailed information about a specific model in the registry`,
		Args:  cobra.ExactArgs(1),
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

func formatBool(value *bool) string {
	if value == nil {
		return ""
	}

	return strconv.FormatBool(*value)
}
