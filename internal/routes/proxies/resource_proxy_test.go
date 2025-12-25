package proxies

import (
	"io"
	"net/url"
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

func Test_mergeExcludedFields(t *testing.T) {
	t.Run("merge missing excluded field", func(t *testing.T) {
		target := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type": "hugging-face",
				"url":  "https://huggingface.co",
			},
		}
		source := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type":        "hugging-face",
				"url":         "https://huggingface.co",
				"credentials": "hf_secret_token_123",
			},
		}
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
		}

		mergeExcludedFields(target, source, excludeFields)

		expected := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type":        "hugging-face",
				"url":         "https://huggingface.co",
				"credentials": "hf_secret_token_123",
			},
		}
		assert.Equal(t, expected, target)
	})

	t.Run("do not override existing excluded field", func(t *testing.T) {
		target := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type":        "hugging-face",
				"url":         "https://huggingface.co",
				"credentials": "new_token",
			},
		}
		source := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type":        "hugging-face",
				"url":         "https://huggingface.co",
				"credentials": "old_token",
			},
		}
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
		}

		mergeExcludedFields(target, source, excludeFields)

		// Should keep the new_token, not override with old_token
		assert.Equal(t, "new_token", target["spec"].(map[string]interface{})["credentials"])
	})

	t.Run("merge multiple excluded fields", func(t *testing.T) {
		target := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type": "updated",
			},
			"status": map[string]interface{}{
				"phase": "Active",
			},
		}
		source := map[string]interface{}{
			"id": "123",
			"spec": map[string]interface{}{
				"type":        "hugging-face",
				"credentials": "hf_token",
			},
			"status": map[string]interface{}{
				"phase":    "Active",
				"sk_value": "secret_key",
			},
		}
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
			"status.sk_value":  {},
		}

		mergeExcludedFields(target, source, excludeFields)

		assert.Equal(t, "hf_token", target["spec"].(map[string]interface{})["credentials"])
		assert.Equal(t, "secret_key", target["status"].(map[string]interface{})["sk_value"])
		assert.Equal(t, "updated", target["spec"].(map[string]interface{})["type"])
	})

	t.Run("handle empty string as missing", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"credentials": "",
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"credentials": "should_be_merged",
			},
		}
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
		}

		mergeExcludedFields(target, source, excludeFields)

		assert.Equal(t, "should_be_merged", target["spec"].(map[string]interface{})["credentials"])
	})

	t.Run("do not merge non-excluded fields", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"type": "new-type",
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"type":        "old-type",
				"credentials": "token",
			},
		}
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
		}

		mergeExcludedFields(target, source, excludeFields)

		// type should not be overridden
		assert.Equal(t, "new-type", target["spec"].(map[string]interface{})["type"])
		// credentials should be merged
		assert.Equal(t, "token", target["spec"].(map[string]interface{})["credentials"])
	})
}

func Test_buildSelectParam(t *testing.T) {
	t.Run("build select param from excluded fields", func(t *testing.T) {
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
			"status.sk_value":  {},
		}

		result := buildSelectParam(excludeFields)

		// Should contain both spec and status (order may vary)
		assert.Contains(t, result, "spec")
		assert.Contains(t, result, "status")
	})

	t.Run("return empty for no excluded fields", func(t *testing.T) {
		excludeFields := map[string]struct{}{}

		result := buildSelectParam(excludeFields)

		assert.Equal(t, "", result)
	})

	t.Run("handle single excluded field", func(t *testing.T) {
		excludeFields := map[string]struct{}{
			"spec.credentials": {},
		}

		result := buildSelectParam(excludeFields)

		assert.Equal(t, "spec", result)
	})
}

func Test_isEmptyValue(t *testing.T) {
	t.Run("nil is empty", func(t *testing.T) {
		assert.True(t, isEmptyValue(nil))
	})

	t.Run("empty string is empty", func(t *testing.T) {
		assert.True(t, isEmptyValue(""))
	})

	t.Run("non-empty string is not empty", func(t *testing.T) {
		assert.False(t, isEmptyValue("hello"))
	})

	t.Run("empty map is empty", func(t *testing.T) {
		assert.True(t, isEmptyValue(map[string]interface{}{}))
	})

	t.Run("non-empty map is not empty", func(t *testing.T) {
		assert.False(t, isEmptyValue(map[string]interface{}{"key": "value"}))
	})

	t.Run("empty array is empty", func(t *testing.T) {
		assert.True(t, isEmptyValue([]interface{}{}))
	})

	t.Run("non-empty array is not empty", func(t *testing.T) {
		assert.False(t, isEmptyValue([]interface{}{1, 2, 3}))
	})

	t.Run("number is not empty", func(t *testing.T) {
		assert.False(t, isEmptyValue(0))
		assert.False(t, isEmptyValue(42))
	})
}

func Test_queryParamsToFilters(t *testing.T) {
	t.Run("convert simple filter", func(t *testing.T) {
		params := url.Values{
			"id": []string{"eq.123"},
		}

		filters := queryParamsToFilters(params)

		assert.Len(t, filters, 1)
		assert.Equal(t, "id", filters[0].Column)
		assert.Equal(t, "eq", filters[0].Operator)
		assert.Equal(t, "123", filters[0].Value)
	})

	t.Run("convert multiple filters", func(t *testing.T) {
		params := url.Values{
			"id":                     []string{"eq.123"},
			"metadata->>name":        []string{"eq.test"},
			"metadata->>workspace":   []string{"eq.default"},
		}

		filters := queryParamsToFilters(params)

		assert.Len(t, filters, 3)
	})

	t.Run("skip reserved parameters", func(t *testing.T) {
		params := url.Values{
			"id":     []string{"eq.123"},
			"select": []string{"spec,status"},
			"order":  []string{"id.desc"},
			"limit":  []string{"10"},
			"offset": []string{"0"},
		}

		filters := queryParamsToFilters(params)

		// Should only have the id filter, not the reserved params
		assert.Len(t, filters, 1)
		assert.Equal(t, "id", filters[0].Column)
	})

	t.Run("handle value without operator", func(t *testing.T) {
		params := url.Values{
			"id": []string{"123"},
		}

		filters := queryParamsToFilters(params)

		assert.Len(t, filters, 1)
		assert.Equal(t, "id", filters[0].Column)
		assert.Equal(t, "eq", filters[0].Operator)
		assert.Equal(t, "123", filters[0].Value)
	})

	t.Run("handle different operators", func(t *testing.T) {
		params := url.Values{
			"id":   []string{"gt.100"},
			"name": []string{"like.*test*"},
			"age":  []string{"lte.30"},
		}

		filters := queryParamsToFilters(params)

		assert.Len(t, filters, 3)

		// Verify operators
		operatorMap := make(map[string]string)
		for _, f := range filters {
			operatorMap[f.Column] = f.Operator
		}

		assert.Equal(t, "gt", operatorMap["id"])
		assert.Equal(t, "like", operatorMap["name"])
		assert.Equal(t, "lte", operatorMap["age"])
	})
}
