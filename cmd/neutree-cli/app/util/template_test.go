package util

import (
	"os"
	"path/filepath"
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

// TestBatchParseTemplateFiles tests the BatchParseTemplateFiles function with various scenarios
func TestBatchParseTemplateFiles(t *testing.T) {
	tempDir := t.TempDir()

	tests := []struct {
		name        string
		template    string
		data        interface{}
		wantContent string
		wantErr     bool
	}{
		{
			name:        "simple string replacement",
			template:    "Hello {{.Name}}!",
			data:        struct{ Name string }{Name: "World"},
			wantContent: "Hello World!",
			wantErr:     false,
		},
		{
			name:        "pointer data",
			template:    "Value: {{.Value}}",
			data:        &struct{ Value int }{Value: 42},
			wantContent: "Value: 42",
			wantErr:     false,
		},
		{
			name:        "invalid template",
			template:    "Hello {{.MissingField}}",
			data:        struct{}{},
			wantContent: "",
			wantErr:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create temporary template file
			tempFile := filepath.Join(tempDir, "test_template.tpl")
			err := os.WriteFile(tempFile, []byte(tt.template), 0644)
			assert.NoError(t, err)
			defer os.Remove(tempFile)

			// Test the function
			err = BatchParseTemplateFiles([]string{tempFile}, tt.data)

			if tt.wantErr {
				assert.Error(t, err)
				return
			}

			assert.NoError(t, err)

			// Verify the file content
			content, err := os.ReadFile(tempFile)
			assert.NoError(t, err)
			assert.Equal(t, tt.wantContent, string(content))
		})
	}
}

// TestBatchParseTemplateFilesErrorCases tests error scenarios
func TestBatchParseTemplateFilesErrorCases(t *testing.T) {
	tempDir := t.TempDir()

	t.Run("non-existent file", func(t *testing.T) {
		nonExistentFile := filepath.Join(tempDir, "nonexistent.tpl")
		err := BatchParseTemplateFiles([]string{nonExistentFile}, struct{}{})
		assert.Error(t, err)
	})

	t.Run("empty file list", func(t *testing.T) {
		err := BatchParseTemplateFiles([]string{}, struct{}{})
		assert.NoError(t, err)
	})
}
