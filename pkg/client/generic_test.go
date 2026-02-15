package client

import (
	"encoding/json"
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
