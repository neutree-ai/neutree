package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils/request"
	"github.com/neutree-ai/neutree/pkg/storage"
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

func Test_extractStructTagConfig_excludeFields(t *testing.T) {
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

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
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

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"metadata.secret": {},
			"status.sk_value": {},
		}
		assert.Equal(t, expected, result.excludeFields)
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

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
	})

	t.Run("no api tag returns empty map", func(t *testing.T) {
		type TestObject struct {
			ID   string `json:"id"`
			Name string `json:"name"`
		}

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		assert.Empty(t, result.excludeFields)
	})

	t.Run("ignore fields with json:-", func(t *testing.T) {
		type TestObject struct {
			ID       string `json:"id"`
			Internal string `json:"-"`
			Secret   string `json:"secret" api:"-"`
		}

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
	})

	t.Run("ignore unexported fields", func(t *testing.T) {
		type TestObject struct {
			ID     string `json:"id"`
			secret string `api:"-"` // unexported, no json tag to avoid go vet error
		}
		obj := TestObject{secret: "hidden"}

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		assert.Equal(t, "hidden", obj.secret)
		assert.Empty(t, result.excludeFields)
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

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"status.secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
	})

	t.Run("extract fields from slice element type", func(t *testing.T) {
		type Auth struct {
			Type       string `json:"type"`
			Credential string `json:"credential" api:"-"`
		}

		type UpstreamEntry struct {
			URL  string `json:"url"`
			Auth *Auth  `json:"auth"`
		}

		type TestObject struct {
			ID        string          `json:"id"`
			Upstreams []UpstreamEntry `json:"upstreams" mergekey:"url"`
		}

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"upstreams.auth.credential": {},
		}
		assert.Equal(t, expected, result.excludeFields)
	})

	t.Run("extract fields from pointer slice element type", func(t *testing.T) {
		type Inner struct {
			Name   string `json:"name"`
			Secret string `json:"secret" api:"-"`
		}

		type TestObject struct {
			Items []*Inner `json:"items" mergekey:"name"`
		}

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"items.secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
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

		result := extractStructTagConfig(reflect.TypeOf(TestObject{}))

		expected := map[string]struct{}{
			"l1.l2.l3.deep_secret": {},
		}
		assert.Equal(t, expected, result.excludeFields)
	})
}

func Test_extractTopLevelJSONFields(t *testing.T) {
	type testObject struct {
		ID       string `json:"id"`
		Name     string `json:"name,omitempty"`
		Ignored  string `json:"-"`
		NoTag    string
		internal string
	}
	obj := testObject{internal: "hidden"}
	assert.Equal(t, "hidden", obj.internal)

	cases := []struct {
		name     string
		input    reflect.Type
		expected []string
	}{
		{
			name:  "extract endpoint writable table columns from API struct",
			input: reflect.TypeOf(v1.Endpoint{}),
			expected: []string{
				"id",
				"api_version",
				"kind",
				"metadata",
				"spec",
				"status",
			},
		},
		{
			name:     "ignore json dash empty and unexported fields",
			input:    reflect.TypeOf(testObject{}),
			expected: []string{"id", "name"},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			fields := extractTopLevelJSONFields(tc.input)

			assert.Equal(t, tc.expected, fields)
			assert.NotContains(t, fields, "status_sort_priority")
		})
	}
}

func TestCreateStructProxyHandlerFiltersUnknownTopLevelFieldsAndInfersColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/endpoints", r.URL.Path)
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.NotContains(t, r.URL.Query(), "columns")
		assert.Equal(t, "*", r.URL.Query().Get("select"))

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Contains(t, payload, "spec")
		assert.Contains(t, payload, "status")
		assert.NotContains(t, payload, "status_sort_priority")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()

	router := gin.New()
	handler := CreateStructProxyHandler[v1.Endpoint](&Dependencies{
		StorageAccessURL: upstream.URL,
	}, storage.ENDPOINT_TABLE)
	router.PATCH("/api/v1/endpoints", handler)

	body := strings.NewReader(`{"id":118,"api_version":"v1","kind":"Endpoint","metadata":{"name":"sshgpu","workspace":"default"},"spec":{"replicas":{"num":0}},"status":{"phase":"Running"},"status_sort_priority":1}`)
	req := httptest.NewRequest(
		http.MethodPatch,
		`/api/v1/endpoints?metadata->>name=eq.sshgpu&metadata->>workspace=eq.default&select=*`,
		body,
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusOK, recorder.ResponseRecorder.Code)
}

func TestCreateStructProxyHandlerFiltersSoftDeletePayloadAndInfersColumns(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/endpoints", r.URL.Path)
		assert.Equal(t, http.MethodPatch, r.Method)
		assert.NotContains(t, r.URL.Query(), "columns")

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Contains(t, payload, "metadata")
		assert.NotContains(t, payload, "spec")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	router := gin.New()
	handler := CreateStructProxyHandler[v1.Endpoint](&Dependencies{
		StorageAccessURL: upstream.URL,
	}, storage.ENDPOINT_TABLE)
	router.PATCH("/api/v1/endpoints", handler)

	body := strings.NewReader(`{"metadata":{"name":"sshgpu","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z","annotations":{"neutree.ai/force-delete":"true"}}}`)
	req := httptest.NewRequest(
		http.MethodPatch,
		`/api/v1/endpoints?id=eq.118`,
		body,
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
}

func TestCreateStructProxyHandlerSkipsBackfillForSoftDeleteOnMaskedResource(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/image_registries", r.URL.Path)
		assert.Equal(t, http.MethodPatch, r.Method)

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.Contains(t, payload, "metadata")
		assert.NotContains(t, payload, "spec")
		assert.NotContains(t, payload, "status")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNoContent)
	}))
	defer upstream.Close()

	router := gin.New()
	handler := CreateStructProxyHandler[v1.ImageRegistry](&Dependencies{
		StorageAccessURL: upstream.URL,
	}, storage.IMAGE_REGISTRY_TABLE)
	router.PATCH("/api/v1/image_registries", handler)

	body := strings.NewReader(`{"metadata":{"name":"registry","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z"}}`)
	req := httptest.NewRequest(
		http.MethodPatch,
		`/api/v1/image_registries?id=eq.118`,
		body,
	)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
}

func TestCreateStructProxyHandlerDoesNotApplyWriteColumnsToPost(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/image_registries", r.URL.Path)
		assert.Equal(t, http.MethodPost, r.Method)
		assert.NotContains(t, r.URL.Query(), "columns")

		var payload map[string]interface{}
		require.NoError(t, json.NewDecoder(r.Body).Decode(&payload))
		assert.NotContains(t, payload, "id")

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusCreated)
		_, _ = w.Write([]byte(`[]`))
	}))
	defer upstream.Close()

	router := gin.New()
	handler := CreateStructProxyHandler[v1.ImageRegistry](&Dependencies{
		StorageAccessURL: upstream.URL,
	}, storage.IMAGE_REGISTRY_TABLE)
	router.POST("/api/v1/image_registries", handler)

	body := strings.NewReader(`{"api_version":"v1","kind":"ImageRegistry","metadata":{"name":"registry","workspace":"default"},"spec":{"url":"https://registry.example.com","repository":"neutree"}}`)
	req := httptest.NewRequest(http.MethodPost, `/api/v1/image_registries`, body)
	req.Header.Set("Content-Type", "application/json")

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, req)

	assert.Equal(t, http.StatusCreated, recorder.ResponseRecorder.Code)
}

type closeNotifyRecorder struct {
	*httptest.ResponseRecorder
	closeCh chan bool
}

func newCloseNotifyRecorder() *closeNotifyRecorder {
	return &closeNotifyRecorder{
		ResponseRecorder: httptest.NewRecorder(),
		closeCh:          make(chan bool, 1),
	}
}

func (r *closeNotifyRecorder) CloseNotify() <-chan bool {
	return r.closeCh
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

		mergeExcludedFields(target, source, excludeFields, nil)

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

		mergeExcludedFields(target, source, excludeFields, nil)

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

		mergeExcludedFields(target, source, excludeFields, nil)

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

		mergeExcludedFields(target, source, excludeFields, nil)

		assert.Equal(t, "should_be_merged", target["spec"].(map[string]interface{})["credentials"])
	})

	t.Run("merge excluded field inside array elements", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{
						"url": "https://api.example.com",
						"auth": map[string]interface{}{
							"type":       "bearer",
							"credential": "",
						},
					},
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{
						"url": "https://api.example.com",
						"auth": map[string]interface{}{
							"type":       "bearer",
							"credential": "sk-secret-token",
						},
					},
				},
			},
		}
		excludeFields := map[string]struct{}{
			"spec.upstreams.auth.credential": {},
		}

		mergeExcludedFields(target, source, excludeFields, nil)

		upstreams := target["spec"].(map[string]interface{})["upstreams"].([]interface{})
		auth := upstreams[0].(map[string]interface{})["auth"].(map[string]interface{})
		assert.Equal(t, "sk-secret-token", auth["credential"])
	})

	t.Run("merge excluded field inside array with multiple elements", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{
						"url": "https://api1.example.com",
						"auth": map[string]interface{}{
							"type":       "bearer",
							"credential": "",
						},
					},
					map[string]interface{}{
						"url": "https://api2.example.com",
						"auth": map[string]interface{}{
							"type":       "api_key",
							"credential": "",
						},
					},
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{
						"url": "https://api1.example.com",
						"auth": map[string]interface{}{
							"type":       "bearer",
							"credential": "token-1",
						},
					},
					map[string]interface{}{
						"url": "https://api2.example.com",
						"auth": map[string]interface{}{
							"type":       "api_key",
							"credential": "token-2",
						},
					},
				},
			},
		}
		excludeFields := map[string]struct{}{
			"spec.upstreams.auth.credential": {},
		}

		mergeExcludedFields(target, source, excludeFields, nil)

		upstreams := target["spec"].(map[string]interface{})["upstreams"].([]interface{})
		auth0 := upstreams[0].(map[string]interface{})["auth"].(map[string]interface{})
		auth1 := upstreams[1].(map[string]interface{})["auth"].(map[string]interface{})
		assert.Equal(t, "token-1", auth0["credential"])
		assert.Equal(t, "token-2", auth1["credential"])
	})

	// NEU-592: credential backfill must pair upstreams by identity, not index.
	upstreamMergeKeys := map[string][]string{
		"spec.upstreams": {"upstream.url", "endpoint_ref"},
	}
	upstreamExcludeFields := map[string]struct{}{
		"spec.upstreams.auth.credential": {},
	}
	externalUpstream := func(url, credential string) map[string]interface{} {
		return map[string]interface{}{
			"upstream": map[string]interface{}{"url": url},
			"auth": map[string]interface{}{
				"type":       "bearer",
				"credential": credential,
			},
		}
	}
	upstreamCredential := func(t *testing.T, target map[string]interface{}, index int) string {
		t.Helper()

		upstreams := target["spec"].(map[string]interface{})["upstreams"].([]interface{})
		auth := upstreams[index].(map[string]interface{})["auth"].(map[string]interface{})

		return auth["credential"].(string)
	}

	t.Run("identity merge survives deleting a leading upstream", func(t *testing.T) {
		// Stored EE: [openrouter (no key), dogfood (key)]; the PATCH deletes the
		// openrouter entry. Index pairing would hand dogfood the deleted entry's
		// empty credential; identity pairing must keep dogfood's own key.
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://dogfood.example.com/v1", ""),
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://openrouter.ai/api/v1", ""),
					externalUpstream("https://dogfood.example.com/v1", "sk_dogfood"),
				},
			},
		}

		mergeExcludedFields(target, source, upstreamExcludeFields, upstreamMergeKeys)

		assert.Equal(t, "sk_dogfood", upstreamCredential(t, target, 0))
	})

	t.Run("identity merge survives reordering upstreams", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://b.example.com/v1", ""),
					externalUpstream("https://a.example.com/v1", ""),
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://a.example.com/v1", "key-a"),
					externalUpstream("https://b.example.com/v1", "key-b"),
				},
			},
		}

		mergeExcludedFields(target, source, upstreamExcludeFields, upstreamMergeKeys)

		assert.Equal(t, "key-b", upstreamCredential(t, target, 0))
		assert.Equal(t, "key-a", upstreamCredential(t, target, 1))
	})

	t.Run("identity merge does not backfill a new upstream", func(t *testing.T) {
		// A brand-new URL has no stored credential; it must stay empty instead of
		// inheriting another entry's key.
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://new.example.com/v1", ""),
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://old.example.com/v1", "old-key"),
				},
			},
		}

		mergeExcludedFields(target, source, upstreamExcludeFields, upstreamMergeKeys)

		assert.Equal(t, "", upstreamCredential(t, target, 0))
	})

	t.Run("identity merge matches endpoint_ref entries and keeps explicit credentials", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					map[string]interface{}{"endpoint_ref": "qwen3-local"},
					externalUpstream("https://a.example.com/v1", "user-typed-new-key"),
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://a.example.com/v1", "stored-key"),
					map[string]interface{}{"endpoint_ref": "qwen3-local"},
				},
			},
		}

		mergeExcludedFields(target, source, upstreamExcludeFields, upstreamMergeKeys)

		// The user-provided replacement key must win over the stored one.
		assert.Equal(t, "user-typed-new-key", upstreamCredential(t, target, 1))
	})

	t.Run("identity merge pairs duplicate identities in order", func(t *testing.T) {
		target := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://dup.example.com/v1", ""),
					externalUpstream("https://dup.example.com/v1", ""),
				},
			},
		}
		source := map[string]interface{}{
			"spec": map[string]interface{}{
				"upstreams": []interface{}{
					externalUpstream("https://dup.example.com/v1", "first"),
					externalUpstream("https://dup.example.com/v1", "second"),
				},
			},
		}

		mergeExcludedFields(target, source, upstreamExcludeFields, upstreamMergeKeys)

		assert.Equal(t, "first", upstreamCredential(t, target, 0))
		assert.Equal(t, "second", upstreamCredential(t, target, 1))
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

		mergeExcludedFields(target, source, excludeFields, nil)

		// type should not be overridden
		assert.Equal(t, "new-type", target["spec"].(map[string]interface{})["type"])
		// credentials should be merged
		assert.Equal(t, "token", target["spec"].(map[string]interface{})["credentials"])
	})
}

func Test_extractStructTagConfig_arrayMergeKeys(t *testing.T) {
	t.Run("external endpoint upstreams declare identity keys", func(t *testing.T) {
		mergeKeys := extractStructTagConfig(reflect.TypeOf(v1.ExternalEndpoint{})).arrayMergeKeys

		assert.Equal(t, []string{"upstream.url", "endpoint_ref"}, mergeKeys["spec.upstreams"])
	})

	t.Run("static node cluster nodes declare identity keys", func(t *testing.T) {
		mergeKeys := extractStructTagConfig(reflect.TypeOf(v1.StaticNodeCluster{})).arrayMergeKeys

		assert.Equal(t, []string{"ip"}, mergeKeys["spec.nodes"])
	})

	t.Run("struct without mergekey tags yields no entries", func(t *testing.T) {
		mergeKeys := extractStructTagConfig(reflect.TypeOf(v1.ApiKey{})).arrayMergeKeys

		assert.Empty(t, mergeKeys)
	})

	t.Run("panics when an array holds a masked field without a mergekey tag", func(t *testing.T) {
		type entry struct {
			Name   string `json:"name"`
			Secret string `json:"secret" api:"-"`
		}

		type spec struct {
			Entries []entry `json:"entries"`
		}

		type object struct {
			Spec spec `json:"spec"`
		}

		assert.Panics(t, func() {
			extractStructTagConfig(reflect.TypeOf(object{}))
		})
	})

	t.Run("does not panic when the whole array is masked", func(t *testing.T) {
		type entry struct {
			Name   string `json:"name"`
			Secret string `json:"secret" api:"-"`
		}

		type spec struct {
			Entries []entry `json:"entries" api:"-"`
		}

		type object struct {
			Spec spec `json:"spec"`
		}

		assert.NotPanics(t, func() {
			extractStructTagConfig(reflect.TypeOf(object{}))
		})
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
			"id":                   []string{"eq.123"},
			"metadata->>name":      []string{"eq.test"},
			"metadata->>workspace": []string{"eq.default"},
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

func TestIsSoftDeleteRequest(t *testing.T) {
	tests := []struct {
		name        string
		requestBody map[string]interface{}
		expected    bool
	}{
		{
			name: "soft delete with metadata.deletion_timestamp set to timestamp string",
			requestBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":               "test-resource",
					"deletion_timestamp": "2025-12-29T06:09:38.917Z",
				},
			},
			expected: true,
		},
		{
			name: "soft delete with metadata.deletion_timestamp set to non-empty value",
			requestBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"deletion_timestamp": "some-value",
				},
			},
			expected: true,
		},
		{
			name: "not a soft delete - no deletion_timestamp field",
			requestBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test-resource",
				},
				"spec": map[string]interface{}{
					"field": "value",
				},
			},
			expected: false,
		},
		{
			name: "not a soft delete - metadata.deletion_timestamp is nil",
			requestBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":               "test-resource",
					"deletion_timestamp": nil,
				},
			},
			expected: false,
		},
		{
			name: "not a soft delete - metadata.deletion_timestamp is empty string",
			requestBody: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name":               "test-resource",
					"deletion_timestamp": "",
				},
			},
			expected: false,
		},
		{
			name:        "empty request body",
			requestBody: map[string]interface{}{},
			expected:    false,
		},
		{
			name: "metadata is not a map",
			requestBody: map[string]interface{}{
				"metadata": "invalid-type",
			},
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := request.IsSoftDeleteRequest(tt.requestBody)
			assert.Equal(t, tt.expected, result, "IsSoftDeleteRequest() should return %v for %s", tt.expected, tt.name)
		})
	}
}
