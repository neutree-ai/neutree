package util

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestJsonEqual(t *testing.T) {
	tests := []struct {
		name     string
		obj1     interface{}
		obj2     interface{}
		expected bool
	}{
		{
			name:     "identical simple objects",
			obj1:     map[string]interface{}{"key": "value"},
			obj2:     map[string]interface{}{"key": "value"},
			expected: true,
		},
		{
			name:     "different values",
			obj1:     map[string]interface{}{"key": "value1"},
			obj2:     map[string]interface{}{"key": "value2"},
			expected: false,
		},
		{
			name:     "nested objects equal",
			obj1:     map[string]interface{}{"nested": map[string]interface{}{"a": "b"}},
			obj2:     map[string]interface{}{"nested": map[string]interface{}{"a": "b"}},
			expected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, diff, err := JsonEqual(tt.obj1, tt.obj2)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result, "diff: %s", diff)
		})
	}
}

func TestNormalizeJSON(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected interface{}
	}{
		{
			name:     "strips null values",
			input:    map[string]interface{}{"a": "1", "b": nil},
			expected: map[string]interface{}{"a": "1"},
		},
		{
			name:     "strips empty maps",
			input:    map[string]interface{}{"a": "1", "b": map[string]interface{}{}},
			expected: map[string]interface{}{"a": "1"},
		},
		{
			name:  "preserves non-empty maps",
			input: map[string]interface{}{"a": map[string]interface{}{"x": "y"}},
			expected: map[string]interface{}{
				"a": map[string]interface{}{"x": "y"},
			},
		},
		{
			name: "strips nulls inside arrays",
			input: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x", "auth": nil},
				},
			},
			expected: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x"},
				},
			},
		},
		{
			name: "strips empty maps inside arrays",
			input: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x", "mapping": map[string]interface{}{}},
				},
			},
			expected: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x"},
				},
			},
		},
		{
			name: "normalizes int to float64 via JSON round-trip",
			input: map[string]interface{}{
				"port": 443,
			},
			expected: map[string]interface{}{
				"port": float64(443),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, err := NormalizeJSON(tt.input)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestJsonMerge(t *testing.T) {
	tests := []struct {
		name     string
		base     interface{}
		patch    interface{}
		expected map[string]interface{}
	}{
		{
			name:  "merge preserves base-only fields",
			base:  map[string]interface{}{"a": "1", "b": "2"},
			patch: map[string]interface{}{"a": "1"},
			expected: map[string]interface{}{
				"a": "1",
				"b": "2",
			},
		},
		{
			name: "merge replaces arrays atomically",
			base: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "old.com"},
				},
			},
			patch: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "new.com"},
				},
			},
			expected: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "new.com"},
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var result map[string]interface{}
			err := JsonMerge(tt.base, tt.patch, &result)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// TestKongPluginConfigStability verifies that normalizing both sides produces
// a stable comparison even when Kong adds null fields or converts nil maps to {}.
func TestKongPluginConfigStability(t *testing.T) {
	// Kong returns config with null fields and empty maps
	kongConfig := map[string]interface{}{
		"route_prefix": "/workspace/default/external-endpoint/test",
		"route_type":   nil, // Kong-added null field
		"upstreams": []interface{}{
			map[string]interface{}{
				"scheme":        "http",
				"host":          "10.255.1.136",
				"port":          float64(8000),
				"path":          "/default/endpoint",
				"model_mapping": map[string]interface{}{}, // Kong converted null → {}
				"auth_header":   nil,                      // Kong stored explicit null
			},
		},
	}

	// Our desired config (Go types, nil values)
	desiredConfig := map[string]interface{}{
		"route_prefix": "/workspace/default/external-endpoint/test",
		"upstreams": []map[string]interface{}{
			{
				"scheme":        "http",
				"host":          "10.255.1.136",
				"port":          8000,                           // int, not float64
				"path":          "/default/endpoint",
				"model_mapping": map[string]string(nil),        // Go nil map
				"auth_header":   nil,                           // Go nil
			},
		},
	}

	normalizedKong, err := NormalizeJSON(kongConfig)
	require.NoError(t, err)

	normalizedDesired, err := NormalizeJSON(desiredConfig)
	require.NoError(t, err)

	result, diff, err := JsonEqual(normalizedKong, normalizedDesired)
	require.NoError(t, err)
	assert.True(t, result, "normalized configs should be equal; diff: %s", diff)
}

// TestAuthRemovalDetected verifies that removing auth from an upstream
// entry is correctly detected even after normalization.
func TestAuthRemovalDetected(t *testing.T) {
	kongConfig := map[string]interface{}{
		"route_prefix": "/test",
		"upstreams": []interface{}{
			map[string]interface{}{
				"host":        "api.example.com",
				"port":        float64(443),
				"auth_header": "Bearer old_token", // non-null → preserved by normalize
			},
		},
	}

	desiredConfig := map[string]interface{}{
		"route_prefix": "/test",
		"upstreams": []map[string]interface{}{
			{
				"host":        "api.example.com",
				"port":        float64(443),
				"auth_header": nil, // user removed auth → stripped by normalize
			},
		},
	}

	normalizedKong, err := NormalizeJSON(kongConfig)
	require.NoError(t, err)

	normalizedDesired, err := NormalizeJSON(desiredConfig)
	require.NoError(t, err)

	result, _, err := JsonEqual(normalizedKong, normalizedDesired)
	require.NoError(t, err)
	assert.False(t, result, "should detect auth removal: Kong has token but desired has nil")
}
