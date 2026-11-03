package client

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestExtractPhase(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "normal phase",
			data: json.RawMessage(`{"status":{"phase":"Running"}}`),
			want: "Running",
		},
		{
			name: "no status field",
			data: json.RawMessage(`{"metadata":{"name":"test"}}`),
			want: "",
		},
		{
			name: "no phase field",
			data: json.RawMessage(`{"status":{"replicas":3}}`),
			want: "",
		},
		{
			name: "invalid JSON",
			data: json.RawMessage(`not json`),
			want: "",
		},
		{
			name: "empty object",
			data: json.RawMessage(`{}`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractPhase(tt.data))
		})
	}
}

func TestIsNotFound(t *testing.T) {
	tests := []struct {
		name string
		err  error
		want bool
	}{
		{
			name: "NotFoundError",
			err:  &NotFoundError{Kind: "Endpoint", Name: "my-ep"},
			want: true,
		},
		{
			name: "wrapped NotFoundError",
			err:  fmt.Errorf("outer: %w", &NotFoundError{Kind: "Endpoint", Name: "my-ep"}),
			want: true,
		},
		{
			name: "other error",
			err:  errors.New("connection refused"),
			want: false,
		},
		{
			name: "nil error",
			err:  nil,
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, IsNotFound(tt.err))
		})
	}
}

func TestNotFoundError_Error(t *testing.T) {
	err := &NotFoundError{Kind: "Endpoint", Name: "my-ep"}
	assert.Equal(t, `Endpoint "my-ep" not found`, err.Error())
}

func TestExtractMetadataField(t *testing.T) {
	data := json.RawMessage(`{
		"metadata": {
			"name": "my-ep",
			"workspace": "default",
			"creation_timestamp": "2025-01-01T00:00:00Z"
		},
		"status": {"phase": "Running"}
	}`)

	tests := []struct {
		name  string
		data  json.RawMessage
		field string
		want  string
	}{
		{
			name:  "extract name",
			data:  data,
			field: "name",
			want:  "my-ep",
		},
		{
			name:  "extract workspace",
			data:  data,
			field: "workspace",
			want:  "default",
		},
		{
			name:  "extract creation_timestamp",
			data:  data,
			field: "creation_timestamp",
			want:  "2025-01-01T00:00:00Z",
		},
		{
			name:  "field not found",
			data:  data,
			field: "nonexistent",
			want:  "",
		},
		{
			name:  "no metadata",
			data:  json.RawMessage(`{"status":{"phase":"Running"}}`),
			field: "name",
			want:  "",
		},
		{
			name:  "invalid JSON",
			data:  json.RawMessage(`not json`),
			field: "name",
			want:  "",
		},
		{
			name:  "non-string field",
			data:  json.RawMessage(`{"metadata":{"count":42}}`),
			field: "count",
			want:  "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractMetadataField(tt.data, tt.field))
		})
	}
}

func TestExtractID(t *testing.T) {
	tests := []struct {
		name string
		data json.RawMessage
		want string
	}{
		{
			name: "uuid string id",
			data: json.RawMessage(`{"id":"721d6adc-6b32-46d1-b4bf-67cb0c576848","metadata":{"name":"k"}}`),
			want: "721d6adc-6b32-46d1-b4bf-67cb0c576848",
		},
		{
			name: "integer id",
			data: json.RawMessage(`{"id":42,"metadata":{"name":"e"}}`),
			want: "42",
		},
		{
			name: "missing id",
			data: json.RawMessage(`{"metadata":{"name":"n"}}`),
			want: "",
		},
		{
			name: "invalid JSON",
			data: json.RawMessage(`not json`),
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, ExtractID(tt.data))
		})
	}
}

func TestIsGlobalKind(t *testing.T) {
	assert.True(t, IsGlobalKind("Workspace"))
	assert.True(t, IsGlobalKind("LogSink"))
	assert.False(t, IsGlobalKind("Endpoint"))
	assert.False(t, IsGlobalKind("Role"))
}

func TestGenericServiceWorkspaceFilterSkippedForGlobalKinds(t *testing.T) {
	tests := []struct {
		kind          string
		path          string
		wantWorkspace bool
	}{
		{kind: "LogSink", path: "/api/v1/log_sinks"},
		{kind: "Workspace", path: "/api/v1/workspaces"},
		{kind: "Endpoint", path: "/api/v1/endpoints", wantWorkspace: true},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			var queries []url.Values

			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, tt.path, r.URL.Path)
				queries = append(queries, r.URL.Query())
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(`[{"id": 1, "metadata": {"name": "a"}}]`))
			}))
			defer server.Close()

			g := NewClient(server.URL).Generic

			_, err := g.Exists(tt.kind, "default", "a")
			assert.NoError(t, err)
			_, err = g.Get(tt.kind, "default", "a")
			assert.NoError(t, err)
			_, err = g.List(tt.kind, "default")
			assert.NoError(t, err)

			assert.Len(t, queries, 3)

			for _, q := range queries {
				_, hasWorkspace := q["metadata->>workspace"]
				assert.Equal(t, tt.wantWorkspace, hasWorkspace)
			}
		})
	}
}
