package wait

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestParseForCondition(t *testing.T) {
	tests := []struct {
		name    string
		input   string
		wantStr string
		wantErr bool
	}{
		{
			name:    "delete",
			input:   "delete",
			wantStr: "delete",
		},
		{
			name:    "jsonpath with leading dot",
			input:   "jsonpath=.status.phase=Running",
			wantStr: "jsonpath=status.phase=Running",
		},
		{
			name:    "jsonpath without leading dot",
			input:   "jsonpath=status.phase=Running",
			wantStr: "jsonpath=status.phase=Running",
		},
		{
			name:    "jsonpath nested path",
			input:   "jsonpath=.metadata.labels.app=myapp",
			wantStr: "jsonpath=metadata.labels.app=myapp",
		},
		{
			name:    "jsonpath missing value",
			input:   "jsonpath=.status.phase=",
			wantErr: true,
		},
		{
			name:    "jsonpath missing path",
			input:   "jsonpath==Running",
			wantErr: true,
		},
		{
			name:    "jsonpath no equals",
			input:   "jsonpath=status.phase",
			wantErr: true,
		},
		{
			name:    "unknown condition",
			input:   "something=else",
			wantErr: true,
		},
		{
			name:    "empty string",
			input:   "",
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cond, err := parseForCondition(tt.input)
			if tt.wantErr {
				assert.Error(t, err)
				assert.Nil(t, cond)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantStr, cond.String())
			}
		})
	}
}

func TestDeleteCondition(t *testing.T) {
	d := deleteCondition{}

	assert.False(t, d.match(json.RawMessage(`{"status":{"phase":"Running"}}`)))
	assert.True(t, d.matchNotFound())
	assert.Equal(t, "delete", d.String())
}

func TestJsonpathCondition(t *testing.T) {
	tests := []struct {
		name  string
		path  string
		value string
		data  json.RawMessage
		want  bool
	}{
		{
			name:  "phase matches",
			path:  "status.phase",
			value: "Running",
			data:  json.RawMessage(`{"status":{"phase":"Running"}}`),
			want:  true,
		},
		{
			name:  "phase matches case-insensitive",
			path:  "status.phase",
			value: "running",
			data:  json.RawMessage(`{"status":{"phase":"Running"}}`),
			want:  true,
		},
		{
			name:  "phase does not match",
			path:  "status.phase",
			value: "Running",
			data:  json.RawMessage(`{"status":{"phase":"Pending"}}`),
			want:  false,
		},
		{
			name:  "nested metadata field",
			path:  "metadata.name",
			value: "my-ep",
			data:  json.RawMessage(`{"metadata":{"name":"my-ep"}}`),
			want:  true,
		},
		{
			name:  "path does not exist",
			path:  "status.phase",
			value: "Running",
			data:  json.RawMessage(`{"metadata":{"name":"test"}}`),
			want:  false,
		},
		{
			name:  "empty data",
			path:  "status.phase",
			value: "Running",
			data:  json.RawMessage(`{}`),
			want:  false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			j := jsonpathCondition{path: tt.path, value: tt.value}
			assert.Equal(t, tt.want, j.match(tt.data))
			assert.False(t, j.matchNotFound())
		})
	}
}
