package model

import (
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

// listColumn binds a column's header to the value it renders. The header and
// the row used to be two independent literals that had to be edited in step;
// pairing them means a new column can only be added in one place.
//
// Headers are the upper-cased JSON names of the fields they show, so a reader
// can map a column onto the API payload — and onto the UI, which names the same
// fields the same way — without a glossary.
type listColumn struct {
	header string
	value  func(model v1.GeneralModel) string
}

// listColumns is the shape of `model list` output.
//
// A model can hold several versions but the row shows one, so the columns below
// the name describe the first version, exactly as they did before the alias
// column existed.
var listColumns = []listColumn{
	{
		header: "NAME",
		value:  func(model v1.GeneralModel) string { return model.Name },
	},
	{
		header: "VERSIONS",
		value:  summariseVersions,
	},
	{
		// An alias is optional, so an empty one renders as unset rather than as
		// unknown: nobody named this version, which is a fact, not a gap.
		header: "ALIAS",
		value: func(model v1.GeneralModel) string {
			return orUnset(versionField(model, func(v v1.ModelVersion) string { return v.Alias }))
		},
	},
	{
		header: "SIZE",
		value: func(model v1.GeneralModel) string {
			return orUnknown(versionField(model, func(v v1.ModelVersion) string { return v.Size }))
		},
	},
	{
		header: "CREATION_TIME",
		value: func(model v1.GeneralModel) string {
			return orUnknown(versionField(model, func(v v1.ModelVersion) string { return v.CreationTime }))
		},
	},
}

type listOptions struct {
	output string
	limit  int
	offset int
}

func NewListCmd() *cobra.Command {
	opts := &listOptions{}

	cmd := &cobra.Command{
		Use:   "list",
		Short: "List all models in the registry",
		Long:  `List all models in the registry with their basic information`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runList(opts)
		},
	}

	addOutputFlag(cmd, &opts.output)
	cmd.Flags().IntVar(&opts.limit, "limit", 0, "Maximum number of models to return (0 uses the server default)")
	cmd.Flags().IntVar(&opts.offset, "offset", 0, "Number of models to skip before the first returned one")

	return cmd
}

func runList(opts *listOptions) error {
	clientOptions := []client.ClientOption{
		client.WithAPIKey(global.APIKey),
	}

	if global.Insecure {
		clientOptions = append(clientOptions, client.WithInsecureSkipVerify())
	}

	// Create client
	c := client.NewClient(global.ServerURL, clientOptions...)
	_, err := c.ModelRegistries.Get(workspace, registry) // Ensure registry exists
	if err != nil {
		return fmt.Errorf("failed to get model registry %s: %w", registry, err)
	}

	// List models
	models, err := c.Models.List(workspace, registry, client.ModelListOptions{
		Limit:  opts.limit,
		Offset: opts.offset,
	})
	if err != nil {
		return fmt.Errorf("failed to list models: %w", err)
	}

	printed, err := printPayload(os.Stdout, opts.output, models.Raw)
	if err != nil || printed {
		return err
	}

	if err := renderModelTable(os.Stdout, models.Models); err != nil {
		return err
	}

	// The page summary is an aside for a human, so it goes to stderr and leaves
	// stdout a clean table.
	fmt.Fprintln(os.Stderr, summarisePage(models))

	return nil
}

// renderModelTable writes the listing as a tab-aligned table.
func renderModelTable(out io.Writer, models []v1.GeneralModel) error {
	w := tabwriter.NewWriter(out, 0, 0, 2, ' ', 0)

	fmt.Fprintln(w, strings.Join(listHeader(), "\t"))

	for _, model := range models {
		fmt.Fprintln(w, strings.Join(listRow(model), "\t"))
	}

	return w.Flush()
}

// listHeader and listRow both walk listColumns, so a column added there lands in
// the header and in every row at once.
func listHeader() []string {
	headers := make([]string, len(listColumns))
	for i, column := range listColumns {
		headers[i] = column.header
	}

	return headers
}

func listRow(model v1.GeneralModel) []string {
	values := make([]string, len(listColumns))
	for i, column := range listColumns {
		values[i] = column.value(model)
	}

	return values
}

// summarisePage describes which slice of the result set was printed. The total
// comes from the Content-Range header and is unknown for a registry that cannot
// count its matches, which is reported as such rather than guessed at.
func summarisePage(models *client.ModelList) string {
	total := unknownValue
	if models.Total != nil {
		total = fmt.Sprintf("%d", *models.Total)
	}

	if len(models.Models) == 0 {
		return fmt.Sprintf("No models shown (total: %s)", total)
	}

	return fmt.Sprintf("Showing models %d-%d (total: %s)",
		models.Offset+1, models.Offset+len(models.Models), total)
}

// summariseVersions names the first version and counts the rest.
func summariseVersions(model v1.GeneralModel) string {
	if len(model.Versions) == 0 {
		return unsetValue
	}

	versions := model.Versions[0].Name
	if len(model.Versions) > 1 {
		versions += fmt.Sprintf(" (+%d more)", len(model.Versions)-1)
	}

	return versions
}

// versionField reads a field off the version the row describes.
func versionField(model v1.GeneralModel, read func(v1.ModelVersion) string) string {
	if len(model.Versions) == 0 {
		return ""
	}

	return read(model.Versions[0])
}

func orUnknown(value string) string {
	if value == "" {
		return unknownValue
	}

	return value
}

func orUnset(value string) string {
	if value == "" {
		return unsetValue
	}

	return value
}
