package v1

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestValidateResourceName(t *testing.T) {
	cases := []struct {
		name    string
		input   string
		message string
	}{
		{name: "accepts lowercase alphanumeric", input: "endpoint1"},
		{name: "accepts dots, hyphens and underscores", input: "ext.open_ai-1"},
		{name: "accepts the maximum length", input: strings.Repeat("a", 63)},
		{name: "rejects an empty name", input: "", message: "external endpoint name is required"},
		{
			name:    "rejects surrounding whitespace before anything else",
			input:   " endpoint ",
			message: "external endpoint name must not contain leading or trailing whitespace",
		},
		{
			name:    "rejects a name past the maximum length",
			input:   strings.Repeat("a", 64),
			message: "external endpoint name must be at most 63 characters",
		},
		{name: "rejects uppercase", input: "Endpoint", message: "external endpoint name must be lowercase"},
		{
			name:    "rejects an inner space",
			input:   "my endpoint",
			message: "external endpoint name must consist of lowercase alphanumeric characters",
		},
		{
			name:    "rejects a non-ascii rune",
			input:   "外部端点",
			message: "external endpoint name must consist of lowercase alphanumeric characters",
		},
		{
			// 30 runes, 90 bytes. What is wrong with it is the character set, and
			// a byte-counted length check would report a length it does not have.
			name:    "faults a long non-ascii name on its characters, not its length",
			input:   strings.Repeat("外", 30),
			message: "external endpoint name must consist of lowercase alphanumeric characters",
		},
		{
			name:    "rejects a trailing hyphen",
			input:   "endpoint-",
			message: "external endpoint name must consist of lowercase alphanumeric characters",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := ValidateResourceName("external endpoint", tc.input)

			if tc.message == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tc.message)
		})
	}
}

// The kind is what makes one shared rule readable in every resource's error.
func TestValidateResourceNameNamesTheKind(t *testing.T) {
	require.Error(t, ValidateResourceName("model", ""))
	assert.Equal(t, "model name is required", ValidateResourceName("model", "").Error())
	assert.Equal(t, "external endpoint name is required", ValidateResourceName("external endpoint", "").Error())
}
