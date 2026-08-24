package controllers

import (
	"strconv"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func newBuiltinRegistryController(storage *storagemocks.MockStorage,
	config model_registry.BuiltinConfig) *WorkspaceController {
	c, _ := NewWorkspaceController(&WorkspaceControllerOption{
		Storage:           storage,
		BuiltinRegistries: config,
	})

	return c
}

func builtinRegistryRow(id int, url string) v1.ModelRegistry {
	return v1.ModelRegistry{
		ID: id,
		Metadata: &v1.Metadata{
			Name:        model_registry.BuiltinHuggingFaceRegistryName,
			Workspace:   "default",
			Annotations: v1.WithBuiltinAnnotation(nil),
		},
		Spec: &v1.ModelRegistrySpec{
			Type: v1.HuggingFaceModelRegistryType,
			Url:  url,
		},
	}
}

func builtinModelScopeRow(id int, url string) v1.ModelRegistry {
	row := builtinRegistryRow(id, url)
	row.Metadata.Name = model_registry.BuiltinModelScopeRegistryName
	row.Spec.Type = v1.ModelScopeModelRegistryType

	return row
}

// provisionedRows installs a ListModelRegistry mock that honours the name filter
// the reconcile sends.
//
// Provisioning walks every built-in kind and looks each one up by name, so a mock
// answering every lookup with the same rows would show each kind the other's
// registry — and the reconcile, seeing the wrong type, would rewrite it. Filtering
// here is what keeps these tests about the behaviour they name.
func provisionedRows(mockStorage *storagemocks.MockStorage, rows ...v1.ModelRegistry) {
	mockStorage.On("ListModelRegistry", mock.Anything).Return(
		func(option storage.ListOption) []v1.ModelRegistry {
			wanted := ""

			for _, filter := range option.Filters {
				if filter.Column == "metadata->name" {
					wanted = filter.Value
				}
			}

			// No name filter means the caller wants the whole workspace, as the
			// teardown path does.
			if wanted == "" {
				return rows
			}

			matched := []v1.ModelRegistry{}

			for _, row := range rows {
				if row.Metadata != nil && strconv.Quote(row.Metadata.Name) == wanted {
					matched = append(matched, row)
				}
			}

			return matched
		}, nil)
}

// defaultRows is what a workspace looks like once provisioning has run with no
// mirrors configured: one registry per built-in kind, at the hub's own address.
func defaultRows() []v1.ModelRegistry {
	return []v1.ModelRegistry{
		builtinRegistryRow(4, model_registry.DefaultHuggingFaceEndpoint),
		builtinModelScopeRow(5, model_registry.DefaultModelScopeEndpoint),
	}
}

func workspaceNamed(name string) v1.Workspace {
	return v1.Workspace{
		ID:       1,
		Metadata: &v1.Metadata{Name: name},
		Status:   &v1.WorkspaceStatus{Phase: v1.WorkspacePhaseCREATED},
	}
}

func TestSyncWorkspaceModelRegistry_ProvisionsWhenEnabled(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage)

	created := map[string]*v1.ModelRegistry{}

	mockStorage.On("CreateModelRegistry", mock.Anything).Run(func(args mock.Arguments) {
		registry, _ := args.Get(0).(*v1.ModelRegistry)
		created[registry.Metadata.Name] = registry
	}).Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://hf-mirror.example",
		ModelScopeEndpoint:  "https://ms-mirror.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	// Every supported public hub, not just the first one.
	require.Len(t, created, 2)

	for name, wantURL := range map[string]string{
		model_registry.BuiltinHuggingFaceRegistryName: "https://hf-mirror.example",
		model_registry.BuiltinModelScopeRegistryName:  "https://ms-mirror.example",
	} {
		registry := created[name]
		require.NotNil(t, registry, "%s was not provisioned", name)
		assert.Equal(t, "default", registry.Metadata.Workspace)
		assert.Equal(t, wantURL, registry.Spec.Url)
		assert.True(t, v1.IsBuiltin(registry.Metadata.Annotations))
	}

	mockStorage.AssertExpectations(t)
}

// An offline deployment is the default, and it must not produce a registry it
// can never reach.
func TestSyncWorkspaceModelRegistry_ProvisionsNothingWhenDisabled(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	// Nothing is even looked up: the switch governs provisioning, so with it off
	// there is nothing to reconcile against.
	mockStorage.AssertNotCalled(t, "ListModelRegistry", mock.Anything)
}

// The switch decides whether registries are provisioned from now on. It does not
// decide whether the ones already there stay: removing a registry that endpoints
// may be serving models from is a decision someone has to be present for, and a
// configuration flag flipped during an upgrade is nobody being present.
func TestSyncWorkspaceModelRegistry_TurningTheSwitchOffLeavesProvisionedRegistries(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return(defaultRows(), nil).Maybe()

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "DeleteModelRegistry", mock.Anything)
}

func TestSyncWorkspaceModelRegistry_RepointsAtANewMirror(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage, defaultRows()...)

	updated := map[string]*v1.ModelRegistry{}

	mockStorage.On("UpdateModelRegistry", mock.Anything, mock.Anything).Run(func(args mock.Arguments) {
		id, _ := args.Get(0).(string)
		registry, _ := args.Get(1).(*v1.ModelRegistry)
		updated[id] = registry
	}).Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://hf-mirror.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	require.NotNil(t, updated["4"])
	assert.Equal(t, "https://hf-mirror.example", updated["4"].Spec.Url)
	// Mirroring one hub does not disturb the other.
	assert.NotContains(t, updated, "5")
	mockStorage.AssertExpectations(t)
}

func TestSyncWorkspaceModelRegistry_LeavesAMatchingRegistryAlone(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage, defaultRows()...)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{Enabled: true})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// Provisioning must never adopt a registry a user created, even one sitting
// under the name the control plane would have used.
func TestSyncWorkspaceModelRegistry_NeverTouchesAUserRegistry(t *testing.T) {
	rows := defaultRows()
	for i := range rows {
		rows[i].Metadata.Annotations = nil
	}

	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage, rows...)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://elsewhere.example",
		ModelScopeEndpoint:  "https://elsewhere.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// A row already on its way out is left to the teardown that is under way.
func TestSyncWorkspaceModelRegistry_SkipsARegistryBeingDeleted(t *testing.T) {
	rows := defaultRows()
	for i := range rows {
		rows[i].Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)
	}

	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage, rows...)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://elsewhere.example",
		ModelScopeEndpoint:  "https://elsewhere.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// Credentials a user attached to the built-in registry are theirs; re-pointing
// the URL must not take them away.
func TestSyncWorkspaceModelRegistry_KeepsUserSuppliedCredentials(t *testing.T) {
	rows := defaultRows()
	rows[0].Spec.Credentials = "hf_token"

	mockStorage := &storagemocks.MockStorage{}
	provisionedRows(mockStorage, rows...)

	var updated *v1.ModelRegistry

	mockStorage.On("UpdateModelRegistry", "4", mock.Anything).Run(func(args mock.Arguments) {
		updated, _ = args.Get(1).(*v1.ModelRegistry)
	}).Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://hf-mirror.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	require.NotNil(t, updated)
	assert.Equal(t, "hf_token", updated.Spec.Credentials)
}

// A workspace being torn down takes its provisioned registries with it, or they
// are left pointing at a workspace that no longer exists — nothing else claims
// them, because the deletion validator deliberately does not count them.
func TestDeleteWorkspaceModelRegistry(t *testing.T) {
	userOwned := builtinRegistryRow(9, "nfs://server/models")
	userOwned.Metadata.Name = "mine"
	userOwned.Metadata.Annotations = nil

	rows := append(defaultRows(), userOwned)

	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return(rows, nil)
	mockStorage.On("DeleteModelRegistry", "4").Return(nil)
	// Every provisioned registry, not just the first kind.
	mockStorage.On("DeleteModelRegistry", "5").Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{Enabled: true})

	workspace := workspaceNamed("default")
	require.NoError(t, c.DeleteWorkspaceModelRegistry(&workspace))

	// Only the provisioned ones. A user's registry is theirs, and workspace
	// deletion is already refused while any remain.
	mockStorage.AssertNotCalled(t, "DeleteModelRegistry", "9")
	mockStorage.AssertExpectations(t)
}
