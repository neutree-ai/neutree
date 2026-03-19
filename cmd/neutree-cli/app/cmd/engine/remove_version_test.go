package engine

import (
	"sort"
	"testing"

	"github.com/stretchr/testify/assert"

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
			sort.Strings(got)
			sort.Strings(tt.want)
			assert.Equal(t, tt.want, got)
		})
	}
}

func TestRemoveVersionFromList(t *testing.T) {
	tests := []struct {
		name         string
		versions     []*v1.EngineVersion
		removeIdx    int
		wantVersions []string
	}{
		{
			name: "remove first",
			versions: []*v1.EngineVersion{
				{Version: "v1.0"},
				{Version: "v2.0"},
				{Version: "v3.0"},
			},
			removeIdx:    0,
			wantVersions: []string{"v2.0", "v3.0"},
		},
		{
			name: "remove middle",
			versions: []*v1.EngineVersion{
				{Version: "v1.0"},
				{Version: "v2.0"},
				{Version: "v3.0"},
			},
			removeIdx:    1,
			wantVersions: []string{"v1.0", "v3.0"},
		},
		{
			name: "remove last",
			versions: []*v1.EngineVersion{
				{Version: "v1.0"},
				{Version: "v2.0"},
				{Version: "v3.0"},
			},
			removeIdx:    2,
			wantVersions: []string{"v1.0", "v2.0"},
		},
		{
			name: "remove only element",
			versions: []*v1.EngineVersion{
				{Version: "v1.0"},
			},
			removeIdx:    0,
			wantVersions: []string{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := append(tt.versions[:tt.removeIdx], tt.versions[tt.removeIdx+1:]...)
			got := make([]string, len(result))
			for i, v := range result {
				got[i] = v.Version
			}
			assert.Equal(t, tt.wantVersions, got)
		})
	}
}

func TestFindVersionIndex(t *testing.T) {
	versions := []*v1.EngineVersion{
		{Version: "v1.0"},
		{Version: "v2.0"},
		{Version: "v3.0"},
	}

	tests := []struct {
		name    string
		target  string
		wantIdx int
	}{
		{"found first", "v1.0", 0},
		{"found middle", "v2.0", 1},
		{"found last", "v3.0", 2},
		{"not found", "v4.0", -1},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			idx := -1
			for i, v := range versions {
				if v.Version == tt.target {
					idx = i
					break
				}
			}
			assert.Equal(t, tt.wantIdx, idx)
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
	sort.Strings(tasks)

	assert.Equal(t, []string{"text-generation", "text-rerank"}, tasks)
	assert.NotContains(t, tasks, "text-embedding")
}
