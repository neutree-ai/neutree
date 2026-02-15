package get

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

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
