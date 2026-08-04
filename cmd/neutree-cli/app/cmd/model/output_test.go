package model

import (
	"bytes"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

// The acceptance this whole flag exists for: what `-o json` prints parses back
// to exactly what the server sent, field for field — including a field this
// build has no Go type for, which a decode/re-encode round trip would drop.
func TestPrintJSONReproducesThePayloadFieldForField(t *testing.T) {
	payload := `{"name":"v1","alias":"pet","info":{"parameter_count":"7615616512",` +
		`"field_sources":{"parameter_count":"auto"},"missing_fields":["quantization"]},"future_field":{"a":[1,2]}}`

	var out bytes.Buffer

	printed, err := printPayload(&out, outputJSON, json.RawMessage(payload))
	require.NoError(t, err)
	require.True(t, printed)

	var want, got any
	require.NoError(t, json.Unmarshal([]byte(payload), &want))
	require.NoError(t, json.Unmarshal(out.Bytes(), &got))
	require.Equal(t, want, got)
}

func TestPrintYAMLReproducesThePayloadFieldForField(t *testing.T) {
	payload := `[{"name":"demo","versions":[{"name":"v1","alias":"pet","future_field":7}]}]`

	var out bytes.Buffer

	printed, err := printPayload(&out, outputYAML, json.RawMessage(payload))
	require.NoError(t, err)
	require.True(t, printed)

	var want, got any
	require.NoError(t, json.Unmarshal([]byte(payload), &want))
	require.NoError(t, yaml.Unmarshal(out.Bytes(), &got))

	// YAML decodes integers as int and JSON as float64, so compare through a
	// common encoding rather than by Go type.
	wantJSON, err := json.Marshal(want)
	require.NoError(t, err)
	gotJSON, err := json.Marshal(got)
	require.NoError(t, err)
	require.JSONEq(t, string(wantJSON), string(gotJSON))
}

// The block style is what makes YAML output worth having over JSON; a
// re-indented copy of the JSON would be pointless.
func TestPrintYAMLEmitsBlockStyle(t *testing.T) {
	var out bytes.Buffer

	_, err := printPayload(&out, outputYAML, json.RawMessage(`{"name":"v1","alias":"pet"}`))
	require.NoError(t, err)
	require.Equal(t, "name: v1\nalias: pet\n", out.String())
}

// A large parameter count must survive as an integer. Decoding JSON numbers into
// interface{} would turn it into a float64 and print it in exponent notation.
func TestPrintYAMLKeepsLargeIntegersExact(t *testing.T) {
	var out bytes.Buffer

	_, err := printPayload(&out, outputYAML, json.RawMessage(`{"parameter_count":671026419200}`))
	require.NoError(t, err)
	require.Equal(t, "parameter_count: 671026419200\n", out.String())
}

// A string that looks like a number has to come back a string, or the output
// stops being a faithful rendering of the payload.
func TestPrintYAMLQuotesAmbiguousStrings(t *testing.T) {
	var out bytes.Buffer

	_, err := printPayload(&out, outputYAML, json.RawMessage(`{"parameter_count":"7615616512","is_moe":"no"}`))
	require.NoError(t, err)
	require.Equal(t, "parameter_count: \"7615616512\"\nis_moe: \"no\"\n", out.String())
}

func TestPrintPayloadLeavesTheTableToTheCaller(t *testing.T) {
	var out bytes.Buffer

	printed, err := printPayload(&out, outputTable, json.RawMessage(`{}`))
	require.NoError(t, err)
	require.False(t, printed)
	require.Empty(t, out.String())
}

func TestPrintPayloadRejectsUnknownFormats(t *testing.T) {
	var out bytes.Buffer

	_, err := printPayload(&out, "toml", json.RawMessage(`{}`))
	require.Error(t, err)
	require.Contains(t, err.Error(), "unsupported output format: toml")
}

// An empty listing is a legitimate result: both formats must emit a parseable
// empty document rather than prose or nothing at all.
func TestPrintPayloadRendersAnEmptyListing(t *testing.T) {
	var out bytes.Buffer

	_, err := printPayload(&out, outputJSON, json.RawMessage(`[]`))
	require.NoError(t, err)
	require.Equal(t, "[]\n", out.String())

	out.Reset()

	_, err = printPayload(&out, outputYAML, json.RawMessage(`[]`))
	require.NoError(t, err)
	require.Equal(t, "[]\n", out.String())
}
