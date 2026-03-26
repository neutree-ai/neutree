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
			name:     "null field vs missing field",
			obj1:     map[string]interface{}{"key": "value", "nullable": nil},
			obj2:     map[string]interface{}{"key": "value"},
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

func TestJsonContains(t *testing.T) {
	tests := []struct {
		name     string
		current  interface{}
		desired  interface{}
		expected bool
	}{
		{
			name:     "identical objects",
			current:  map[string]interface{}{"key": "value"},
			desired:  map[string]interface{}{"key": "value"},
			expected: true,
		},
		{
			name:     "current has extra fields",
			current:  map[string]interface{}{"key": "value", "extra": "field"},
			desired:  map[string]interface{}{"key": "value"},
			expected: true,
		},
		{
			name:     "desired has field not in current",
			current:  map[string]interface{}{"key": "value"},
			desired:  map[string]interface{}{"key": "value", "missing": "field"},
			expected: false,
		},
		{
			name:     "different values for same key",
			current:  map[string]interface{}{"key": "value1"},
			desired:  map[string]interface{}{"key": "value2"},
			expected: false,
		},
		{
			name: "nested objects - current has extras",
			current: map[string]interface{}{
				"nested": map[string]interface{}{"a": "b", "extra": "c"},
			},
			desired: map[string]interface{}{
				"nested": map[string]interface{}{"a": "b"},
			},
			expected: true,
		},
		{
			name: "array elements - current has extra fields per element",
			current: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x", "port": float64(443), "timeout": float64(60000)},
				},
			},
			desired: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x", "port": float64(443)},
				},
			},
			expected: true,
		},
		{
			name: "array length mismatch",
			current: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x"},
				},
			},
			desired: map[string]interface{}{
				"items": []interface{}{
					map[string]interface{}{"host": "x"},
					map[string]interface{}{"host": "y"},
				},
			},
			expected: false,
		},
		{
			name: "int vs float64 normalization via JSON round-trip",
			current: map[string]interface{}{
				"port": float64(443),
			},
			desired: map[string]interface{}{
				"port": 443, // Go int, normalized to float64 by JSON round-trip
			},
			expected: true,
		},
		{
			name: "desired nil field - current missing",
			current: map[string]interface{}{
				"key": "value",
			},
			desired: map[string]interface{}{
				"key":         "value",
				"auth_header": nil,
			},
			expected: true,
		},
		{
			name: "desired nil field - current has value",
			current: map[string]interface{}{
				"key":         "value",
				"auth_header": "Bearer xxx",
			},
			desired: map[string]interface{}{
				"key":         "value",
				"auth_header": nil,
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result, diff, err := JsonContains(tt.current, tt.desired)
			require.NoError(t, err)
			assert.Equal(t, tt.expected, result, "diff: %s", diff)
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

// TestKongDefaultFieldsInArraysCausesContinuousUpdate demonstrates the root cause:
// Kong adds default fields to plugin config array entries. Since JSON Merge Patch (RFC 7386)
// replaces arrays atomically, those defaults are lost during merge, causing a persistent diff.
// The fix uses JsonContains (subset comparison) instead of JsonEqual for the comparison step.
func TestKongDefaultFieldsInArraysCausesContinuousUpdate(t *testing.T) {
	// Kong returns config with default fields added to upstream entries
	kongConfig := map[string]interface{}{
		"route_prefix": "/workspace/ws/external-endpoint/ep",
		"upstreams": []interface{}{
			map[string]interface{}{
				"scheme":          "https",
				"host":            "api.example.com",
				"port":            float64(443),
				"path":            "/v1",
				"model_mapping":   map[string]interface{}{"gpt-4": "gpt-4"},
				"connect_timeout": float64(60000), // Kong-added default
				"send_timeout":    float64(60000), // Kong-added default
			},
		},
	}

	// Our desired config (only includes fields we manage)
	desiredConfig := map[string]interface{}{
		"route_prefix": "/workspace/ws/external-endpoint/ep",
		"upstreams": []map[string]interface{}{
			{
				"scheme":        "https",
				"host":          "api.example.com",
				"port":          float64(443),
				"path":          "/v1",
				"model_mapping": map[string]string{"gpt-4": "gpt-4"},
			},
		},
	}

	// OLD behavior: JsonMerge + JsonEqual → continuous update loop
	var merged map[string]interface{}
	err := JsonMerge(kongConfig, desiredConfig, &merged)
	require.NoError(t, err)

	equalResult, _, err := JsonEqual(kongConfig, merged)
	require.NoError(t, err)
	assert.False(t, equalResult, "JsonEqual after merge should detect diff due to lost Kong defaults in array")

	// NEW behavior: JsonContains → stable comparison (no false positive)
	containsResult, _, err := JsonContains(kongConfig, desiredConfig)
	require.NoError(t, err)
	assert.True(t, containsResult, "JsonContains should confirm Kong's config already contains all desired fields")
}
