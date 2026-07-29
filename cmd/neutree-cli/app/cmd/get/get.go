package get

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"
	"time"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
	"github.com/neutree-ai/neutree/pkg/client"
)

// --- options & command ---

type getOptions struct {
	workspace string
	output    string // table, json, yaml
	watch     bool
	interval  time.Duration
	timeout   time.Duration
}

// NewGetCmd creates the get cobra command.
func NewGetCmd() *cobra.Command {
	opts := &getOptions{}

	cmd := &cobra.Command{
		Use:   "get <KIND> [NAME]",
		Short: "Get resources",
		Long: `Get one or more resources by kind.

If a NAME is provided, get the specific resource. Otherwise, list all resources of that kind.
Use --watch to continuously print updates.

Examples:
  # List all endpoints in the default workspace
  neutree-cli get endpoint -w default

  # Get a specific endpoint as JSON
  neutree-cli get endpoint my-ep -w default -o json

  # List all workspaces
  neutree-cli get workspace

  # Watch an endpoint continuously
  neutree-cli get endpoint my-ep -w default --watch

  # Watch with custom interval
  neutree-cli get endpoint my-ep -w default --watch --interval 10s`,
		Args:          cobra.RangeArgs(1, 2),
		SilenceUsage:  true,
		SilenceErrors: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runGet(opts, args)
		},
	}

	cmd.Flags().StringVarP(&opts.workspace, "workspace", "w", "default", "Workspace name (ignored for Workspace kind)")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "table", "Output format: table, json, yaml")
	cmd.Flags().BoolVar(&opts.watch, "watch", false, "Watch for changes and print updates")
	cmd.Flags().DurationVar(&opts.interval, "interval", 5*time.Second, "Watch poll interval")
	cmd.Flags().DurationVar(&opts.timeout, "timeout", 5*time.Minute, "Watch timeout")

	return cmd
}

// --- run logic ---

func runGet(opts *getOptions, args []string) error {
	c, err := global.NewClient()
	if err != nil {
		return err
	}

	kind, err := c.Generic.ResolveKind(args[0])
	if err != nil {
		return err
	}

	var name string
	if len(args) == 2 {
		name = args[1]
	}

	printer := &resourcePrinter{kind: kind, format: opts.output, single: name != ""}

	if opts.watch {
		return runWatch(c, kind, name, opts, printer)
	}

	return runOnce(c, kind, name, opts, printer)
}

func runOnce(c *client.Client, kind, name string, opts *getOptions, printer *resourcePrinter) error {
	items, err := fetchResources(c, kind, name, opts.workspace)
	if err != nil {
		return err
	}

	// Empty results are handed to the printer like any other result so that
	// machine formats emit a parseable empty document instead of prose.
	return printer.print(items)
}

func runWatch(c *client.Client, kind, name string, opts *getOptions, printer *resourcePrinter) error {
	refresh := func() error {
		items, err := fetchResources(c, kind, name, opts.workspace)
		if err != nil {
			return err
		}

		clearScreen()

		printer.headerPrinted = false

		return printer.print(items)
	}

	if err := refresh(); err != nil {
		return err
	}

	ticker := time.NewTicker(opts.interval)
	defer ticker.Stop()

	// Treat non-positive timeout as no timeout (watch indefinitely).
	var timerC <-chan time.Time

	if opts.timeout > 0 {
		timer := time.NewTimer(opts.timeout)
		defer timer.Stop()

		timerC = timer.C
	}

	for {
		select {
		case <-timerC:
			return nil
		case <-ticker.C:
			if err := refresh(); err != nil {
				return err
			}
		}
	}
}

// clearScreen moves the cursor to the top-left and clears the terminal.
func clearScreen() {
	fmt.Print("\033[H\033[2J")
}

// --- data fetching ---

func fetchResources(c *client.Client, kind, name, workspace string) ([]json.RawMessage, error) {
	if name != "" {
		data, err := c.Generic.Get(kind, workspace, name)
		if err != nil {
			return nil, err
		}

		return []json.RawMessage{data}, nil
	}

	return c.Generic.List(kind, workspace)
}

// --- output formatting ---

// resourcePrinter handles output formatting with state (e.g., table header printed once).
type resourcePrinter struct {
	kind   string
	format string
	// single reports whether a NAME was requested. It picks the output envelope,
	// which follows the request form rather than the result count: a list request
	// always renders a collection (even for one item), a named request always
	// renders a bare object.
	single        bool
	headerPrinted bool
	// out is the destination for rendered output; defaults to os.Stdout when nil
	// (overridden in tests to capture output).
	out io.Writer
	// errOut receives human-readable notices that must stay off stdout;
	// defaults to os.Stderr when nil.
	errOut io.Writer
}

// writer returns the printer's output destination, defaulting to os.Stdout.
func (p *resourcePrinter) writer() io.Writer {
	if p.out != nil {
		return p.out
	}

	return os.Stdout
}

// errWriter returns the destination for human-readable notices, defaulting to os.Stderr.
func (p *resourcePrinter) errWriter() io.Writer {
	if p.errOut != nil {
		return p.errOut
	}

	return os.Stderr
}

func (p *resourcePrinter) print(items []json.RawMessage) error {
	// Formatters below never see a nil slice: an absent result and an empty one
	// render identically, and a nil slice would otherwise encode as JSON `null`.
	if items == nil {
		items = []json.RawMessage{}
	}

	switch p.format {
	case "json":
		return p.printJSON(items)
	case "yaml":
		return p.printYAML(items)
	case "table":
		return p.printTable(items)
	default:
		return fmt.Errorf("unsupported output format: %s", p.format)
	}
}

func (p *resourcePrinter) printTable(items []json.RawMessage) error {
	w := tabwriter.NewWriter(p.writer(), 0, 4, 2, ' ', 0)

	if !p.headerPrinted {
		if p.kind == "Workspace" {
			fmt.Fprintln(w, "NAME\tID\tPHASE\tAGE")
		} else {
			fmt.Fprintln(w, "NAME\tID\tWORKSPACE\tPHASE\tAGE")
		}

		p.headerPrinted = true
	}

	// The "nothing here" notice is a human-readable aside, so it goes to stderr:
	// stdout stays a clean, uniformly shaped stream across every output format.
	if len(items) == 0 {
		fmt.Fprintf(p.errWriter(), "No %s resources found\n", strings.ToLower(p.kind))
	}

	for _, item := range items {
		name := client.ExtractMetadataField(item, "name")
		id := client.ExtractID(item)
		workspace := client.ExtractMetadataField(item, "workspace")
		phase := client.ExtractPhase(item)
		age := formatAge(client.ExtractMetadataField(item, "creation_timestamp"))

		if p.kind == "Workspace" {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", name, id, phase, age)
		} else {
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", name, id, workspace, phase, age)
		}
	}

	return w.Flush()
}

func (p *resourcePrinter) printJSON(items []json.RawMessage) error {
	var output any

	if p.single {
		if len(items) == 0 {
			return nil
		}

		output = items[0]
	} else {
		output = items
	}

	enc := json.NewEncoder(p.writer())
	enc.SetIndent("", "  ")

	return enc.Encode(output)
}

// printYAML renders one YAML document, mirroring the JSON envelope: a list
// request yields a sequence, a named request yields a mapping. A document
// stream would be neither — an empty one parses as null rather than as a
// collection, and a one-item one is indistinguishable from a named result.
func (p *resourcePrinter) printYAML(items []json.RawMessage) error {
	decode := func(item json.RawMessage) (any, error) {
		var obj any
		if err := json.Unmarshal(item, &obj); err != nil {
			return nil, fmt.Errorf("failed to parse JSON: %w", err)
		}

		return obj, nil
	}

	var output any

	if p.single {
		if len(items) == 0 {
			return nil
		}

		obj, err := decode(items[0])
		if err != nil {
			return err
		}

		output = obj
	} else {
		list := make([]any, 0, len(items))

		for _, item := range items {
			obj, err := decode(item)
			if err != nil {
				return err
			}

			list = append(list, obj)
		}

		output = list
	}

	out, err := yaml.Marshal(output)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	_, err = fmt.Fprint(p.writer(), string(out))

	return err
}

// --- utilities ---

func formatAge(timestamp string) string {
	if timestamp == "" {
		return "<unknown>"
	}

	t, err := time.Parse(time.RFC3339, timestamp)
	if err != nil {
		t, err = time.Parse("2006-01-02T15:04:05", timestamp)
		if err != nil {
			return "<unknown>"
		}
	}

	d := time.Since(t)
	if d < 0 {
		d = 0
	}

	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 24*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}
