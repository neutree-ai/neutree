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
		{
			name: "array with null field vs array without field",
			obj1: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "example.com", "auth_header": nil},
				},
			},
			obj2: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "example.com"},
				},
			},
			expected: false,
		},
		{
			name: "array entries identical without null fields",
			obj1: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "example.com", "port": float64(443)},
				},
			},
			obj2: map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"host": "example.com", "port": float64(443)},
				},
			},
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

// TestNullFieldCausesInstableComparison demonstrates that explicit null values
// in plugin configs can cause instable comparisons when Kong strips nulls on return.
// This is the root cause of continuous plugin update loops.
func TestNullFieldCausesInstableComparison(t *testing.T) {
	// Simulate what Kong returns (no null fields)
	kongConfig := map[string]interface{}{
		"route_prefix": "/workspace/ws/external-endpoint/ep",
		"upstreams": []interface{}{
			map[string]interface{}{
				"scheme": "https",
				"host":   "api.example.com",
				"port":   float64(443),
				"path":   "/v1",
			},
		},
	}

	// Desired config WITHOUT explicit nil values (the fix)
	desiredConfigFixed := map[string]interface{}{
		"route_prefix": "/workspace/ws/external-endpoint/ep",
		"upstreams": []map[string]interface{}{
			{
				"scheme": "https",
				"host":   "api.example.com",
				"port":   443,
				"path":   "/v1",
			},
		},
	}

	// After merge, the desired config should match Kong's config
	var mergedFixed map[string]interface{}
	err := JsonMerge(kongConfig, desiredConfigFixed, &mergedFixed)
	require.NoError(t, err)

	result, _, err := JsonEqual(kongConfig, mergedFixed)
	require.NoError(t, err)
	assert.True(t, result, "fixed config should be stable: no diff after merge with Kong's config")
}
