package proxies

import (
	"io"
	"reflect"
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
			"status.sk_value": {},
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

func Test_extractExcludeFieldsFromTag(t *testing.T) {
	t.Run("extract single field with api tag", func(t *testing.T) {
		type TestStatus struct {
			Phase   string `json:"phase"`
			Secret  string `json:"secret" api:"-"`
			Message string `json:"message"`
		}

		type TestObject struct {
			ID     string     `json:"id"`
			Status TestStatus `json:"status"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("extract multiple fields with api tag", func(t *testing.T) {
		type TestMetadata struct {
			Name   string `json:"name"`
			Secret string `json:"secret" api:"-"`
		}

		type TestStatus struct {
			Phase   string `json:"phase"`
			SKValue string `json:"sk_value" api:"-"`
		}

		type TestObject struct {
			ID       string       `json:"id"`
			Metadata TestMetadata `json:"metadata"`
			Status   TestStatus   `json:"status"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"metadata.secret": {},
			"status.sk_value": {},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("handle pointer types", func(t *testing.T) {
		type TestStatus struct {
			Phase  string `json:"phase"`
			Secret string `json:"secret" api:"-"`
		}

		type TestObject struct {
			ID     string      `json:"id"`
			Status *TestStatus `json:"status"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("no api tag returns empty map", func(t *testing.T) {
		type TestObject struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		assert.Empty(t, result)
	})

	t.Run("ignore fields with json:-", func(t *testing.T) {
		type TestObject struct {
			ID       string `json:"id"`
			Internal string `json:"-"`
			Secret   string `json:"secret" api:"-"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"secret": {},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("ignore unexported fields", func(t *testing.T) {
		type TestObject struct {
			ID     string `json:"id"`
			secret string `api:"-"` // unexported, no json tag to avoid go vet error
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		assert.Empty(t, result)
	})

	t.Run("handle json tag with omitempty", func(t *testing.T) {
		type TestStatus struct {
			Phase  string `json:"phase,omitempty"`
			Secret string `json:"secret,omitempty" api:"-"`
		}

		type TestObject struct {
			ID     string     `json:"id"`
			Status TestStatus `json:"status"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result)
	})

	t.Run("deeply nested structs", func(t *testing.T) {
		type Level3 struct {
			DeepSecret string `json:"deep_secret" api:"-"`
		}

		type Level2 struct {
			L3 Level3 `json:"l3"`
		}

		type Level1 struct {
			L2 Level2 `json:"l2"`
		}

		type TestObject struct {
			ID string `json:"id"`
			L1 Level1 `json:"l1"`
		}

		result := extractExcludeFieldsFromTag(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"l1.l2.l3.deep_secret": {},
		}
		assert.Equal(t, expected, result)
	})
}
