package request

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestExtractBody(t *testing.T) {
	tests := []struct {
		name        string
		body        string
		wantErr     bool
		expectedMap map[string]interface{}
	}{
		{
			name: "valid JSON body",
			body: `{"metadata":{"name":"test"},"spec":{"field":"value"}}`,
			expectedMap: map[string]interface{}{
				"metadata": map[string]interface{}{
					"name": "test",
				},
				"spec": map[string]interface{}{
					"field": "value",
				},
			},
			wantErr: false,
		},
		{
			name:    "invalid JSON body",
			body:    `{"invalid": json}`,
			wantErr: true,
		},
		{
			name:        "empty body",
			body:        `{}`,
			expectedMap: map[string]interface{}{},
			wantErr:     false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gin.SetMode(gin.TestMode)
			w := httptest.NewRecorder()
			c, _ := gin.CreateTestContext(w)

			c.Request = &http.Request{
				Body: io.NopCloser(bytes.NewBufferString(tt.body)),
			}

			ctx, err := ExtractBody(c)

			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, ctx)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.body, string(ctx.BodyBytes))
				assert.Equal(t, tt.expectedMap, ctx.BodyMap)
			}
		})
	}
}

func TestRestoreBody(t *testing.T) {
	gin.SetMode(gin.TestMode)
	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)

	originalBody := []byte(`{"test":"data"}`)
	c.Request = &http.Request{
		Header: make(http.Header),
	}

	RestoreBody(c, originalBody)

	// Read the restored body
	restoredBody, err := io.ReadAll(c.Request.Body)
	require.NoError(t, err)

	assert.Equal(t, originalBody, restoredBody)
	assert.Equal(t, int64(len(originalBody)), c.Request.ContentLength)
	assert.Equal(t, "15", c.Request.Header.Get("Content-Length"))
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
			result := IsSoftDeleteRequest(tt.requestBody)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractFilterValue(t *testing.T) {
	tests := []struct {
		name     string
		filter   string
		expected string
	}{
		{
			name:     "eq operator",
			filter:   "eq.my-workspace",
			expected: "my-workspace",
		},
		{
			name:     "like operator",
			filter:   "like.*test*",
			expected: "*test*",
		},
		{
			name:     "gt operator",
			filter:   "gt.100",
			expected: "100",
		},
		{
			name:     "empty string",
			filter:   "",
			expected: "",
		},
		{
			name:     "no dot - return whole string",
			filter:   "value",
			expected: "value",
		},
		{
			name:     "dot at end",
			filter:   "eq.",
			expected: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := ExtractFilterValue(tt.filter)
			assert.Equal(t, tt.expected, result)
		})
	}
}

func TestExtractResourceIdentifiers(t *testing.T) {
	tests := []struct {
		name             string
		queryParams      url.Values
		tableName        string
		expectedWorkspace string
		expectedName     string
		wantErr          bool
	}{
		{
			name: "cluster with workspace and name",
			queryParams: url.Values{
				"metadata->>workspace": []string{"eq.default"},
				"metadata->>name":      []string{"eq.my-cluster"},
			},
			tableName:        storage.CLUSTERS_TABLE,
			expectedWorkspace: "default",
			expectedName:     "my-cluster",
			wantErr:          false,
		},
		{
			name: "workspace with only name",
			queryParams: url.Values{
				"metadata->>name": []string{"eq.my-workspace"},
			},
			tableName:        storage.WORKSPACE_TABLE,
			expectedWorkspace: "",
			expectedName:     "my-workspace",
			wantErr:          false,
		},
		{
			name: "user_profile with id",
			queryParams: url.Values{
				"id": []string{"eq.user-123"},
			},
			tableName:        storage.USER_PROFILE_TABLE,
			expectedWorkspace: "",
			expectedName:     "user-123",
			wantErr:          false,
		},
		{
			name: "missing workspace for cluster",
			queryParams: url.Values{
				"metadata->>name": []string{"eq.my-cluster"},
			},
			tableName: storage.CLUSTERS_TABLE,
			wantErr:      true,
		},
		{
			name: "missing name for workspace",
			queryParams: url.Values{
				"other-param": []string{"value"},
			},
			tableName: storage.WORKSPACE_TABLE,
			wantErr:      true,
		},
		{
			name: "missing id for user_profile",
			queryParams: url.Values{
				"other-param": []string{"value"},
			},
			tableName: storage.USER_PROFILE_TABLE,
			wantErr:      true,
		},
		{
			name: "image_registry with workspace and name",
			queryParams: url.Values{
				"metadata->>workspace": []string{"eq.default"},
				"metadata->>name":      []string{"eq.my-registry"},
			},
			tableName:        storage.IMAGE_REGISTRY_TABLE,
			expectedWorkspace: "default",
			expectedName:     "my-registry",
			wantErr:          false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workspace, name, err := ExtractResourceIdentifiers(tt.queryParams, tt.tableName)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				require.NoError(t, err)
				assert.Equal(t, tt.expectedWorkspace, workspace)
				assert.Equal(t, tt.expectedName, name)
			}
		})
	}
}
