package engine

import (
	"encoding/json"
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestCollectSupportedTasks(t *testing.T) {
	tests := []struct {
		name     string
		versions []*v1.EngineVersion
		want     []string
	}{
		{
			name:     "no versions",
			versions: nil,
			want:     []string{},
		},
		{
			name: "single version single task",
			versions: []*v1.EngineVersion{
				{Version: "v1.0", SupportedTasks: []string{"text-generation"}},
			},
			want: []string{"text-generation"},
		},
		{
			name: "multiple versions with overlapping tasks",
			versions: []*v1.EngineVersion{
				{Version: "v1.0", SupportedTasks: []string{"text-generation", "text-embedding"}},
				{Version: "v2.0", SupportedTasks: []string{"text-generation", "text-rerank"}},
			},
			want: []string{"text-embedding", "text-generation", "text-rerank"},
		},
		{
			name: "version with no tasks",
			versions: []*v1.EngineVersion{
				{Version: "v1.0", SupportedTasks: []string{"text-generation"}},
				{Version: "v2.0", SupportedTasks: nil},
			},
			want: []string{"text-generation"},
		},
		{
			name: "all versions with same tasks",
			versions: []*v1.EngineVersion{
				{Version: "v1.0", SupportedTasks: []string{"text-generation"}},
				{Version: "v2.0", SupportedTasks: []string{"text-generation"}},
			},
			want: []string{"text-generation"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := collectSupportedTasks(tt.versions)
			sort.Strings(tt.want)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestCollectSupportedTasks_AfterRemoval(t *testing.T) {
	// Simulate: engine has 3 versions, remove the one with a unique task
	versions := []*v1.EngineVersion{
		{Version: "v1.0", SupportedTasks: []string{"text-generation"}},
		{Version: "v2.0", SupportedTasks: []string{"text-generation", "text-embedding"}},
		{Version: "v3.0", SupportedTasks: []string{"text-generation", "text-rerank"}},
	}

	// Remove v2.0 (the only version with text-embedding)
	remaining := append(versions[:1], versions[2:]...)
	tasks := collectSupportedTasks(remaining)

	assert.Equal(t, []string{"text-generation", "text-rerank"}, tasks)
	assert.NotContains(t, tasks, "text-embedding")
}

func TestMatchEndpointsByEngineVersion(t *testing.T) {
	tests := []struct {
		name       string
		endpoints  []v1.Endpoint
		engineName string
		version    string
		wantInUse  bool
		wantNames  []string
		wantErr    bool
	}{
		{
			name:       "no endpoints",
			endpoints:  nil,
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  false,
			wantNames:  nil,
		},
		{
			name: "no match",
			endpoints: []v1.Endpoint{
				makeEndpoint("ep1", "vllm", "v2.0"),
				makeEndpoint("ep2", "llama-cpp", "v1.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  false,
			wantNames:  nil,
		},
		{
			name: "single match",
			endpoints: []v1.Endpoint{
				makeEndpoint("ep1", "vllm", "v1.0"),
				makeEndpoint("ep2", "vllm", "v2.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  true,
			wantNames:  []string{"ep1"},
		},
		{
			name: "multiple matches",
			endpoints: []v1.Endpoint{
				makeEndpoint("ep1", "vllm", "v1.0"),
				makeEndpoint("ep2", "vllm", "v1.0"),
				makeEndpoint("ep3", "vllm", "v2.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  true,
			wantNames:  []string{"ep1", "ep2"},
		},
		{
			name: "endpoint with nil engine spec",
			endpoints: []v1.Endpoint{
				{Metadata: &v1.Metadata{Name: "ep1"}, Spec: &v1.EndpointSpec{Engine: nil}},
				makeEndpoint("ep2", "vllm", "v1.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  true,
			wantNames:  []string{"ep2"},
		},
		{
			name: "same engine different version",
			endpoints: []v1.Endpoint{
				makeEndpoint("ep1", "vllm", "v2.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  false,
			wantNames:  nil,
		},
		{
			name: "same version different engine",
			endpoints: []v1.Endpoint{
				makeEndpoint("ep1", "llama-cpp", "v1.0"),
			},
			engineName: "vllm",
			version:    "v1.0",
			wantInUse:  false,
			wantNames:  nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			items := make([]json.RawMessage, len(tt.endpoints))

			for i, ep := range tt.endpoints {
				data, err := json.Marshal(ep)
				require.NoError(t, err)

				items[i] = data
			}

			inUse, names, err := matchEndpointsByEngineVersion(items, tt.engineName, tt.version)

			if tt.wantErr {
				assert.Error(t, err)
			} else {
				assert.NoError(t, err)
				assert.Equal(t, tt.wantInUse, inUse)
				assert.Equal(t, tt.wantNames, names)
			}
		})
	}
}

func TestMatchEndpointsByEngineVersion_InvalidJSON(t *testing.T) {
	items := []json.RawMessage{[]byte(`{invalid json`)}

	_, _, err := matchEndpointsByEngineVersion(items, "vllm", "v1.0")
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to decode endpoint")
}

// makeEndpoint creates an Endpoint with the given name, engine, and version.
func makeEndpoint(name, engineName, version string) v1.Endpoint {
	return v1.Endpoint{
		Metadata: &v1.Metadata{Name: name},
		Spec: &v1.EndpointSpec{
			Engine: &v1.EndpointEngineSpec{
				Engine:  engineName,
				Version: version,
			},
		},
	}
}
