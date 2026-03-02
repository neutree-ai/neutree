package resource

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/pkg/scheme"
)

// fakeObject implements scheme.Object for testing.
type fakeObject struct {
	kind      string
	name      string
	workspace string
}

func (f *fakeObject) GetKind() string                   { return f.kind }
func (f *fakeObject) SetKind(string)                    {}
func (f *fakeObject) GetName() string                   { return f.name }
func (f *fakeObject) GetWorkspace() string              { return f.workspace }
func (f *fakeObject) GetLabels() map[string]string      { return nil }
func (f *fakeObject) SetLabels(map[string]string)       {}
func (f *fakeObject) GetAnnotations() map[string]string { return nil }
func (f *fakeObject) SetAnnotations(map[string]string)  {}
func (f *fakeObject) GetCreationTimestamp() string       { return "" }
func (f *fakeObject) GetUpdateTimestamp() string         { return "" }
func (f *fakeObject) GetDeletionTimestamp() string       { return "" }
func (f *fakeObject) GetMetadata() any           { return nil }
func (f *fakeObject) GetSpec() any               { return nil }
func (f *fakeObject) GetStatus() any             { return nil }
func (f *fakeObject) GetID() string                      { return "" }

func TestPriorityOf(t *testing.T) {
	tests := []struct {
		kind string
		want int
	}{
		{"Workspace", 0},
		{"Engine", 1},
		{"Cluster", 2},
		{"Endpoint", 3},
		{"ExternalEndpoint", 3},
		{"UnknownKind", 99},
	}

	for _, tt := range tests {
		t.Run(tt.kind, func(t *testing.T) {
			assert.Equal(t, tt.want, PriorityOf(tt.kind))
		})
	}
}

func TestLabel(t *testing.T) {
	tests := []struct {
		name      string
		kind      string
		workspace string
		resName   string
		want      string
	}{
		{
			name:      "with workspace",
			kind:      "Endpoint",
			workspace: "default",
			resName:   "my-ep",
			want:      "Endpoint/default/my-ep",
		},
		{
			name:    "without workspace",
			kind:    "Workspace",
			resName: "my-ws",
			want:    "Workspace/my-ws",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, Label(tt.kind, tt.workspace, tt.resName))
		})
	}
}

func TestSortByPriority(t *testing.T) {
	resources := []scheme.Object{
		&fakeObject{kind: "Endpoint", name: "ep1"},
		&fakeObject{kind: "Workspace", name: "ws1"},
		&fakeObject{kind: "Cluster", name: "cl1"},
		&fakeObject{kind: "Engine", name: "eng1"},
	}

	SortByPriority(resources)

	kinds := make([]string, len(resources))
	for i, r := range resources {
		kinds[i] = r.GetKind()
	}

	assert.Equal(t, []string{"Workspace", "Engine", "Cluster", "Endpoint"}, kinds)
}

func TestSortByReversePriority(t *testing.T) {
	resources := []scheme.Object{
		&fakeObject{kind: "Workspace", name: "ws1"},
		&fakeObject{kind: "Endpoint", name: "ep1"},
		&fakeObject{kind: "Endpoint", name: "ep2"},
		&fakeObject{kind: "Cluster", name: "cl1"},
	}

	SortByReversePriority(resources)

	// Verify reverse order
	kinds := make([]string, len(resources))
	for i, r := range resources {
		kinds[i] = r.GetKind()
	}
	assert.Equal(t, []string{"Endpoint", "Endpoint", "Cluster", "Workspace"}, kinds)

	// Verify stable: ep1 before ep2 (original order preserved)
	assert.Equal(t, "ep1", resources[0].GetName())
	assert.Equal(t, "ep2", resources[1].GetName())
}
