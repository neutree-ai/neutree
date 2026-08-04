package model

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"

	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

// Output formats accepted by the model subcommands, matching `neutree-cli get`.
const (
	outputTable = "table"
	outputJSON  = "json"
	outputYAML  = "yaml"
)

// Placeholders for the two ways a rendered field can carry no value. They are
// deliberately different strings: "the registry could not establish this" and
// "nobody has set this" are different facts, and a reader who cannot tell them
// apart will read an unparsed checkpoint as a deliberately blank one.
const (
	// unknownValue renders a field named in the payload's missing_fields.
	unknownValue = "unknown"
	// unsetValue renders a field that is simply not set, an alias above all.
	unsetValue = "-"
)

// addOutputFlag registers the -o/--output flag shared by the model subcommands.
func addOutputFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "output", "o", outputTable,
		fmt.Sprintf("Output format: %s, %s, %s", outputTable, outputJSON, outputYAML))
}

// printPayload renders the server's response in the requested machine-readable
// format, or reports false when the format asks for the human-readable table
// and the caller has to render it itself.
//
// Both formats are re-serialised from the response bytes rather than from a
// decoded struct, so every field the server sent survives verbatim, in the order
// it sent them, whether or not this build has a Go field for it. That is what
// makes `-o json` usable as the check that the CLI, the API and the UI name the
// same things the same way.
func printPayload(w io.Writer, format string, raw json.RawMessage) (bool, error) {
	switch format {
	case outputTable:
		return false, nil
	case outputJSON:
		return true, printJSON(w, raw)
	case outputYAML:
		return true, printYAML(w, raw)
	default:
		return true, fmt.Errorf("unsupported output format: %s", format)
	}
}

// printJSON re-indents the response body. json.Indent only inserts whitespace,
// so the payload itself is untouched.
func printJSON(w io.Writer, raw json.RawMessage) error {
	var buf bytes.Buffer
	if err := json.Indent(&buf, raw, "", "  "); err != nil {
		return fmt.Errorf("failed to parse response as JSON: %w", err)
	}

	buf.WriteByte('\n')

	_, err := w.Write(buf.Bytes())

	return err
}

// printYAML converts the response body to YAML.
//
// The conversion goes through a yaml.Node rather than a map: a map loses the
// server's field order, and decoding JSON numbers into interface{} turns them
// into float64, which would print a large parameter count in exponent notation.
// YAML 1.2 is a superset of JSON, so the parser reads the body directly.
func printYAML(w io.Writer, raw json.RawMessage) error {
	var node yaml.Node
	if err := yaml.Unmarshal(raw, &node); err != nil {
		return fmt.Errorf("failed to parse response as JSON: %w", err)
	}

	// An empty document decodes to a zero node that the encoder rejects.
	if node.Kind == 0 {
		return nil
	}

	plainStyle(&node)

	out, err := yaml.Marshal(&node)
	if err != nil {
		return fmt.Errorf("failed to marshal YAML: %w", err)
	}

	_, err = w.Write(out)

	return err
}

// plainStyle clears the flow and quoting styles the parser recorded while
// reading the JSON body, so the output is block-style YAML instead of a
// re-indented copy of the JSON.
func plainStyle(node *yaml.Node) {
	node.Style = 0

	for _, child := range node.Content {
		plainStyle(child)
	}
}
