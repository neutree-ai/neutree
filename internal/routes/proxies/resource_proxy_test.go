package proxies

import (
	"io"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func Test_filterObject(t *testing.T) {
	t.Run("exclude top-level field", func(t *testing.T) {
		obj := map[string]interface{}{
			"id":   "123",
			"name": "test",
		}
		excludeFields := map[string]struct{}{
			"name": {},
		}

		result := filterObject(obj, excludeFields, "")

		assert.Equal(t, map[string]interface{}{
			"id": "123",
		}, result)
	})

	t.Run("exclude nested field", func(t *testing.T) {
		obj := map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase":    "Active",
				"sk_value": "secret123",
			},
		}
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result := filterObject(obj, excludeFields, "")

		assert.Equal(t, map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase": "Active",
			},
		}, result)
	})

	t.Run("exclude multiple fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase":    "Active",
				"sk_value": "secret123",
			},
			"metadata": map[string]interface{}{
				"name":   "test",
				"secret": "hidden",
			},
		}
		excludeFields := map[string]struct{}{
			"status.sk_value":  {},
			"metadata.secret": {},
		}

		result := filterObject(obj, excludeFields, "")

		assert.Equal(t, map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase": "Active",
			},
			"metadata": map[string]interface{}{
				"name": "test",
			},
		}, result)
	})

	t.Run("preserve non-matching fields", func(t *testing.T) {
		obj := map[string]interface{}{
			"id":   "123",
			"name": "test",
			"status": map[string]interface{}{
				"phase":    "Active",
				"sk_value": "secret123",
				"usage":    int64(100),
			},
		}
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result := filterObject(obj, excludeFields, "")

		assert.Equal(t, map[string]interface{}{
			"id":   "123",
			"name": "test",
			"status": map[string]interface{}{
				"phase": "Active",
				"usage": int64(100),
			},
		}, result)
	})

	t.Run("filter objects in array", func(t *testing.T) {
		obj := map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"id": "1",
					"status": map[string]interface{}{
						"phase":    "Active",
						"sk_value": "secret1",
					},
				},
				map[string]interface{}{
					"id": "2",
					"status": map[string]interface{}{
						"phase":    "Pending",
						"sk_value": "secret2",
					},
				},
			},
		}
		excludeFields := map[string]struct{}{
			"items.status.sk_value": {},
		}

		result := filterObject(obj, excludeFields, "")

		expected := map[string]interface{}{
			"items": []interface{}{
				map[string]interface{}{
					"id": "1",
					"status": map[string]interface{}{
						"phase": "Active",
					},
				},
				map[string]interface{}{
					"id": "2",
					"status": map[string]interface{}{
						"phase": "Pending",
					},
				},
			},
		}
		assert.Equal(t, expected, result)
	})
}

func Test_filterJSONFields(t *testing.T) {
	t.Run("input is object", func(t *testing.T) {
		data := map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase":    "Active",
				"sk_value": "secret",
			},
		}
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result := filterJSONFields(data, excludeFields)

		assert.Equal(t, map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"phase": "Active",
			},
		}, result)
	})

	t.Run("input is array", func(t *testing.T) {
		data := []interface{}{
			map[string]interface{}{
				"id": "1",
				"status": map[string]interface{}{
					"phase":    "Active",
					"sk_value": "secret1",
				},
			},
			map[string]interface{}{
				"id": "2",
				"status": map[string]interface{}{
					"phase":    "Pending",
					"sk_value": "secret2",
				},
			},
		}
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result := filterJSONFields(data, excludeFields)

		expected := []interface{}{
			map[string]interface{}{
				"id": "1",
				"status": map[string]interface{}{
					"phase": "Active",
				},
			},
			map[string]interface{}{
				"id": "2",
				"status": map[string]interface{}{
					"phase": "Pending",
				},
			},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("input is primitive type", func(t *testing.T) {
		data := "simple string"
		excludeFields := map[string]struct{}{
			"any.field": {},
		}

		result := filterJSONFields(data, excludeFields)

		assert.Equal(t, "simple string", result)
	})

	t.Run("empty excludeFields returns original data", func(t *testing.T) {
		data := map[string]interface{}{
			"id": "123",
			"status": map[string]interface{}{
				"sk_value": "should-keep",
			},
		}
		excludeFields := map[string]struct{}{}

		result := filterJSONFields(data, excludeFields)

		assert.Equal(t, data, result)
	})
}

func Test_filterResponseBody(t *testing.T) {
	t.Run("filter normal JSON response", func(t *testing.T) {
		jsonBody := `{"id":"123","status":{"phase":"Active","sk_value":"secret"}}`
		body := io.NopCloser(strings.NewReader(jsonBody))
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result, err := filterResponseBody(body, excludeFields)

		require.NoError(t, err)
		assert.JSONEq(t, `{"id":"123","status":{"phase":"Active"}}`, string(result))
	})

	t.Run("empty response body", func(t *testing.T) {
		body := io.NopCloser(strings.NewReader(""))
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result, err := filterResponseBody(body, excludeFields)

		require.NoError(t, err)
		assert.Empty(t, result)
	})

	t.Run("invalid JSON returns error", func(t *testing.T) {
		jsonBody := `{"id":"123", invalid json}`
		body := io.NopCloser(strings.NewReader(jsonBody))
		excludeFields := map[string]struct{}{
			"status.sk_value": {},
		}

		result, err := filterResponseBody(body, excludeFields)

		assert.Error(t, err)
		assert.Nil(t, result)
		assert.Contains(t, err.Error(), "failed to unmarshal response body")
	})
}
