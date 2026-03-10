package delete

import (
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/resource"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

func TestValidateArgs(t *testing.T) {
	tests := []struct {
		name    string
		opts    *deleteOptions
		args    []string
		wantErr string
	}{
		{
			name:    "valid kind+name",
			opts:    &deleteOptions{},
			args:    []string{"Endpoint", "my-ep"},
			wantErr: "",
		},
		{
			name:    "valid file mode",
			opts:    &deleteOptions{file: "resources.yaml"},
			args:    nil,
			wantErr: "",
		},
		{
			name:    "file and args mutually exclusive",
			opts:    &deleteOptions{file: "resources.yaml"},
			args:    []string{"Endpoint", "my-ep"},
			wantErr: "cannot specify both -f/--file and positional arguments",
		},
		{
			name:    "no file and no args",
			opts:    &deleteOptions{},
			args:    nil,
			wantErr: "exactly 2 arguments required",
		},
		{
			name:    "no file and one arg",
			opts:    &deleteOptions{},
			args:    []string{"Endpoint"},
			wantErr: "exactly 2 arguments required",
		},
		{
			name:    "no file and three args",
			opts:    &deleteOptions{},
			args:    []string{"Endpoint", "my-ep", "extra"},
			wantErr: "exactly 2 arguments required",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateArgs(tt.opts, tt.args)
			if tt.wantErr == "" {
				assert.NoError(t, err)
			} else {
				assert.ErrorContains(t, err, tt.wantErr)
			}
		})
	}
}

func TestSortByReversePriority(t *testing.T) {
	resources := []scheme.Object{
		&fakeObject{kind: "Workspace", name: "ws1"},
		&fakeObject{kind: "Endpoint", name: "ep1"},
		&fakeObject{kind: "Cluster", name: "cl1"},
		&fakeObject{kind: "Engine", name: "eng1"},
		&fakeObject{kind: "Endpoint", name: "ep2"},
	}

	resource.SortByReversePriority(resources)

	// Endpoints (priority 3) should come first, then Cluster (2), then Engine (1), then Workspace (0)
	kinds := make([]string, len(resources))
	for i, r := range resources {
		kinds[i] = r.GetKind()
	}

	assert.Equal(t, []string{"Endpoint", "Endpoint", "Cluster", "Engine", "Workspace"}, kinds)
}

func TestSortByReversePriority_StableOrder(t *testing.T) {
	resources := []scheme.Object{
		&fakeObject{kind: "Endpoint", name: "ep-b"},
		&fakeObject{kind: "Endpoint", name: "ep-a"},
		&fakeObject{kind: "Cluster", name: "cl1"},
	}

	resource.SortByReversePriority(resources)

	// Same-priority items should preserve original order (stable sort)
	assert.Equal(t, "ep-b", resources[0].GetName())
	assert.Equal(t, "ep-a", resources[1].GetName())
	assert.Equal(t, "cl1", resources[2].GetName())
}

func TestSortByReversePriority_UnknownKinds(t *testing.T) {
	resources := []scheme.Object{
		&fakeObject{kind: "Workspace", name: "ws1"},
		&fakeObject{kind: "UnknownKind", name: "unk1"},
		&fakeObject{kind: "Endpoint", name: "ep1"},
	}

	resource.SortByReversePriority(resources)

	// Unknown kinds get priority 99, so they come first in reverse order
	kinds := make([]string, len(resources))
	for i, r := range resources {
		kinds[i] = r.GetKind()
	}

	assert.Equal(t, []string{"UnknownKind", "Endpoint", "Workspace"}, kinds)
}

func TestResourceLabel(t *testing.T) {
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
			name:      "without workspace",
			kind:      "Workspace",
			workspace: "",
			resName:   "my-ws",
			want:      "Workspace/my-ws",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, resource.Label(tt.kind, tt.workspace, tt.resName))
		})
	}
}

// fakeObject implements scheme.Object for testing sort logic.
type fakeObject struct {
	kind      string
	name      string
	workspace string
}

func (f *fakeObject) GetKind() string                     { return f.kind }
func (f *fakeObject) SetKind(string)                      {}
func (f *fakeObject) GetName() string                     { return f.name }
func (f *fakeObject) GetWorkspace() string                { return f.workspace }
func (f *fakeObject) GetLabels() map[string]string        { return nil }
func (f *fakeObject) SetLabels(map[string]string)         {}
func (f *fakeObject) GetAnnotations() map[string]string   { return nil }
func (f *fakeObject) SetAnnotations(map[string]string)    {}
func (f *fakeObject) GetCreationTimestamp() string         { return "" }
func (f *fakeObject) GetUpdateTimestamp() string           { return "" }
func (f *fakeObject) GetDeletionTimestamp() string         { return "" }
func (f *fakeObject) GetMetadata() interface{}             { return nil }
func (f *fakeObject) GetSpec() interface{}                 { return nil }
func (f *fakeObject) GetStatus() interface{}               { return nil }
func (f *fakeObject) GetID() string                        { return "" }
