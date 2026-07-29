package get

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestPrintTableIncludesIDColumn(t *testing.T) {
	var buf bytes.Buffer

	p := &resourcePrinter{kind: "ApiKey", format: "table", out: &buf}

	items := []json.RawMessage{
		json.RawMessage(`{"id":"721d6adc-6b32-46d1-b4bf-67cb0c576848","metadata":{"name":"tos-titlegen-provider","workspace":"default"},"status":{"phase":"ACTIVE"}}`),
	}
	require.NoError(t, p.printTable(items))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)

	// Header carries an ID column between NAME and WORKSPACE.
	assert.Equal(t, []string{"NAME", "ID", "WORKSPACE", "PHASE", "AGE"}, strings.Fields(lines[0]))
	row := strings.Fields(lines[1])
	assert.Equal(t, "tos-titlegen-provider", row[0])
	assert.Equal(t, "721d6adc-6b32-46d1-b4bf-67cb0c576848", row[1])
	assert.Equal(t, "default", row[2])
	assert.Equal(t, "ACTIVE", row[3])
}

func TestPrintTableWorkspaceKindOmitsWorkspaceColumnButKeepsID(t *testing.T) {
	var buf bytes.Buffer

	p := &resourcePrinter{kind: "Workspace", format: "table", out: &buf}

	items := []json.RawMessage{
		json.RawMessage(`{"id":7,"metadata":{"name":"default"},"status":{"phase":"READY"}}`),
	}
	require.NoError(t, p.printTable(items))

	lines := strings.Split(strings.TrimSpace(buf.String()), "\n")
	require.Len(t, lines, 2)
	assert.Equal(t, []string{"NAME", "ID", "PHASE", "AGE"}, strings.Fields(lines[0]))
	row := strings.Fields(lines[1])
	assert.Equal(t, "default", row[0])
	assert.Equal(t, "7", row[1])
	assert.Equal(t, "READY", row[2])
}

// itemA and itemB stand in for resources returned by the API.
var (
	itemA = json.RawMessage(`{"id":1,"metadata":{"name":"a","workspace":"default"},"status":{"phase":"READY"}}`)
	itemB = json.RawMessage(`{"id":2,"metadata":{"name":"b","workspace":"default"},"status":{"phase":"READY"}}`)
)

// TestOutputMatrix covers empty/single/multiple results across every output
// format, asserting that machine formats always emit a parseable document and
// that the envelope follows resourcePrinter.single (NEU-578).
func TestOutputMatrix(t *testing.T) {
	tests := []struct {
		name   string
		format string
		single bool
		items  []json.RawMessage
		// assert inspects stdout; stderr is checked separately per case.
		assert func(t *testing.T, stdout string)
		stderr string
	}{
		{
			name:   "json list empty is an empty array",
			format: "json",
			items:  []json.RawMessage{},
			assert: func(t *testing.T, stdout string) {
				var got []any
				require.NoError(t, json.Unmarshal([]byte(stdout), &got))
				assert.Empty(t, got)
			},
		},
		{
			name:   "json list nil slice is an empty array not null",
			format: "json",
			items:  nil,
			assert: func(t *testing.T, stdout string) {
				assert.Equal(t, "[]", strings.TrimSpace(stdout))
			},
		},
		{
			name:   "json list of one stays an array",
			format: "json",
			items:  []json.RawMessage{itemA},
			assert: func(t *testing.T, stdout string) {
				var got []map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &got))
				require.Len(t, got, 1)
				assert.Equal(t, "a", got[0]["metadata"].(map[string]any)["name"])
			},
		},
		{
			name:   "json list of many is an array",
			format: "json",
			items:  []json.RawMessage{itemA, itemB},
			assert: func(t *testing.T, stdout string) {
				var got []map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &got))
				assert.Len(t, got, 2)
			},
		},
		{
			name:   "json named request is a bare object",
			format: "json",
			single: true,
			items:  []json.RawMessage{itemA},
			assert: func(t *testing.T, stdout string) {
				var got map[string]any
				require.NoError(t, json.Unmarshal([]byte(stdout), &got))
				assert.Equal(t, "a", got["metadata"].(map[string]any)["name"])
			},
		},
		{
			name:   "yaml list empty is an empty sequence",
			format: "yaml",
			items:  []json.RawMessage{},
			assert: func(t *testing.T, stdout string) {
				var got []any
				require.NoError(t, yaml.Unmarshal([]byte(stdout), &got))
				assert.NotNil(t, got, "empty YAML output must parse as a sequence, not null")
				assert.Empty(t, got)
			},
		},
		{
			name:   "yaml list of one stays a sequence",
			format: "yaml",
			items:  []json.RawMessage{itemA},
			assert: func(t *testing.T, stdout string) {
				var got []map[string]any
				require.NoError(t, yaml.Unmarshal([]byte(stdout), &got))
				require.Len(t, got, 1)
				assert.Equal(t, "a", got[0]["metadata"].(map[string]any)["name"])
			},
		},
		{
			name:   "yaml list of many is one sequence document",
			format: "yaml",
			items:  []json.RawMessage{itemA, itemB},
			assert: func(t *testing.T, stdout string) {
				var got []map[string]any
				require.NoError(t, yaml.Unmarshal([]byte(stdout), &got))
				assert.Len(t, got, 2)
				assert.NotContains(t, stdout, "---", "list output must be one document, not a stream")

				// A single document: the decoder sees exactly one and then EOF.
				dec := yaml.NewDecoder(strings.NewReader(stdout))
				require.NoError(t, dec.Decode(new(any)))
				assert.Error(t, dec.Decode(new(any)))
			},
		},
		{
			name:   "yaml named request is a bare mapping",
			format: "yaml",
			single: true,
			items:  []json.RawMessage{itemA},
			assert: func(t *testing.T, stdout string) {
				var got map[string]any
				require.NoError(t, yaml.Unmarshal([]byte(stdout), &got))
				assert.Equal(t, "a", got["metadata"].(map[string]any)["name"])
			},
		},
		{
			name:   "table empty keeps stdout structural and notices go to stderr",
			format: "table",
			items:  []json.RawMessage{},
			assert: func(t *testing.T, stdout string) {
				lines := strings.Split(strings.TrimSpace(stdout), "\n")
				require.Len(t, lines, 1)
				assert.Equal(t, []string{"NAME", "ID", "WORKSPACE", "PHASE", "AGE"}, strings.Fields(lines[0]))
				assert.NotContains(t, stdout, "No ")
			},
			stderr: "No apikey resources found\n",
		},
		{
			name:   "table non-empty writes nothing to stderr",
			format: "table",
			items:  []json.RawMessage{itemA},
			assert: func(t *testing.T, stdout string) {
				assert.Len(t, strings.Split(strings.TrimSpace(stdout), "\n"), 2)
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer

			p := &resourcePrinter{
				kind:   "ApiKey",
				format: tt.format,
				single: tt.single,
				out:    &stdout,
				errOut: &stderr,
			}
			require.NoError(t, p.print(tt.items))

			tt.assert(t, stdout.String())
			assert.Equal(t, tt.stderr, stderr.String())
		})
	}
}

// TestEmptyOutputDiffersAcrossFormats guards the shape of the original NEU-578
// regression, which TestOutputMatrix cannot express per-format: every format
// emitted byte-identical prose on an empty result. Only the cross-format
// comparison lives here; what each format renders is asserted in the matrix.
func TestEmptyOutputDiffersAcrossFormats(t *testing.T) {
	rendered := map[string]string{}

	for _, format := range []string{"table", "json", "yaml"} {
		var stdout, stderr bytes.Buffer

		p := &resourcePrinter{kind: "ModelCatalog", format: format, out: &stdout, errOut: &stderr}
		require.NoError(t, p.print([]json.RawMessage{}))

		rendered[format] = stdout.String()
	}

	assert.NotEqual(t, rendered["table"], rendered["json"])
	assert.NotEqual(t, rendered["table"], rendered["yaml"])
	// json and yaml legitimately coincide: `[]` is both valid JSON and a valid
	// YAML flow sequence.
}

func TestFormatAge(t *testing.T) {
	tests := []struct {
		name      string
		timestamp string
		want      string
	}{
		{
			name:      "empty timestamp",
			timestamp: "",
			want:      "<unknown>",
		},
		{
			name:      "invalid timestamp",
			timestamp: "not-a-date",
			want:      "<unknown>",
		},
		{
			name:      "RFC3339 format parses",
			timestamp: "2020-01-01T00:00:00Z",
			want:      "", // just check it doesn't return <unknown>
		},
		{
			name:      "no timezone format parses",
			timestamp: "2020-01-01T00:00:00",
			want:      "", // just check it doesn't return <unknown>
		},
		{
			name:      "future timestamp clamps to 0s",
			timestamp: "2099-01-01T00:00:00Z",
			want:      "0s",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatAge(tt.timestamp)
			if tt.want == "" {
				// For valid timestamps, just verify it returns a non-unknown value
				// (exact value depends on current time)
				assert.NotEqual(t, "<unknown>", result)
				assert.Regexp(t, `^\d+[smhd]$`, result)
			} else {
				assert.Equal(t, tt.want, result)
			}
		})
	}
}
