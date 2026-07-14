package get

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
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
