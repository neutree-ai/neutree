package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseTemplate(t *testing.T) {
	tests := []struct {
		name        string
		templateStr string
		data        interface{}
		expected    string
		expectError bool
	}{
		{
			name:        "simple template",
			templateStr: "Hello {{.Name}}!",
			data:        struct{ Name string }{Name: "World"},
			expected:    "Hello World!",
			expectError: false,
		},
		{
			name:        "template with empty lines",
			templateStr: "Line1\n\nLine2\n  \nLine3",
			data:        nil,
			expected:    "Line1\nLine2\nLine3",
			expectError: false,
		},
		{
			name:        "invalid template syntax",
			templateStr: "Hello {{.Name}!", // Missing closing brace
			data:        struct{ Name string }{Name: "World"},
			expectError: true,
		},
		{
			name:        "template with complex data",
			templateStr: "User: {{.User.Name}}, Age: {{.User.Age}}",
			data: struct {
				User struct {
					Name string
					Age  int
				}
			}{
				User: struct {
					Name string
					Age  int
				}{
					Name: "Alice",
					Age:  30,
				},
			},
			expected:    "User: Alice, Age: 30",
			expectError: false,
		},
		{
			name:        "empty template",
			templateStr: "",
			data:        nil,
			expected:    "",
			expectError: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := ParseTemplate(tt.templateStr, tt.data)

			if tt.expectError {
				assert.Error(t, err)
				assert.Nil(t, result)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.expected, string(result))
		})
	}
}

func TestRemoveEmptyLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "no empty lines",
			input:    "Line1\nLine2\nLine3",
			expected: "Line1\nLine2\nLine3",
		},
		{
			name:     "with empty lines",
			input:    "Line1\n\nLine2\n  \nLine3",
			expected: "Line1\nLine2\nLine3",
		},
		{
			name:     "all empty lines",
			input:    "\n\n  \n\t\n",
			expected: "",
		},
		{
			name:     "empty string",
			input:    "",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := removeEmptyLines(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
