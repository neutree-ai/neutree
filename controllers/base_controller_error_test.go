package controllers

import (
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
)

func TestFormatErrorForStatus(t *testing.T) {
	tests := []struct {
		name     string
		err      error
		expected string
	}{
		{
			name:     "nil error returns empty string",
			err:      nil,
			expected: "",
		},
		{
			name:     "simple error",
			err:      errors.New("connection timeout"),
			expected: "connection timeout",
		},
		{
			name: "single wrapped error",
			err: errors.Wrap(
				errors.New("connection timeout"),
				"failed to connect",
			),
			expected: "failed to connect: connection timeout",
		},
		{
			name: "double wrapped error",
			err: errors.Wrap(
				errors.Wrap(
					errors.New("connection timeout"),
					"failed to connect",
				),
				"failed to authenticate",
			),
			expected: "failed to authenticate: failed to connect: connection timeout",
		},
		{
			name: "wrapped error with Wrapf",
			err: errors.Wrapf(
				errors.New("invalid credentials"),
				"failed to authenticate with registry %s/%s",
				"ws1", "reg1",
			),
			expected: "failed to authenticate with registry ws1/reg1: invalid credentials",
		},
		{
			name: "multiple layers with Wrapf",
			err: errors.Wrapf(
				errors.Wrapf(
					errors.New("dial tcp: connection refused"),
					"failed to connect to %s",
					"registry.example.com",
				),
				"failed to authenticate with image registry %s/%s",
				"ws1", "reg1",
			),
			expected: "failed to authenticate with image registry ws1/reg1: failed to connect to registry.example.com: dial tcp: connection refused",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := FormatErrorForStatus(tt.err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestFormatStatusTime(t *testing.T) {
	result := FormatStatusTime()
	assert.NotEmpty(t, result)
	// Check that it's in RFC3339Nano format (has nanoseconds)
	assert.Contains(t, result, "T")
	assert.Contains(t, result, ".")
}
