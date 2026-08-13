package controllers

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
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

func workspaceNamed(name string) v1.Workspace {
	return v1.Workspace{
		ID:       1,
		Metadata: &v1.Metadata{Name: name},
		Status:   &v1.WorkspaceStatus{Phase: v1.WorkspacePhaseCREATED},
	}
}

func TestSyncWorkspaceModelRegistry_ProvisionsWhenEnabled(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{}, nil)

	var created *v1.ModelRegistry

	mockStorage.On("CreateModelRegistry", mock.Anything).Run(func(args mock.Arguments) {
		created, _ = args.Get(0).(*v1.ModelRegistry)
	}).Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://hf-mirror.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	require.NotNil(t, created)
	assert.Equal(t, model_registry.BuiltinHuggingFaceRegistryName, created.Metadata.Name)
	assert.Equal(t, "default", created.Metadata.Workspace)
	assert.Equal(t, "https://hf-mirror.example", created.Spec.Url)
	assert.True(t, v1.IsBuiltin(created.Metadata.Annotations))

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
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return([]v1.ModelRegistry{builtinRegistryRow(4, "https://huggingface.co")}, nil).Maybe()

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "DeleteModelRegistry", mock.Anything)
}

func TestSyncWorkspaceModelRegistry_RepointsAtANewMirror(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return([]v1.ModelRegistry{builtinRegistryRow(4, "https://huggingface.co")}, nil)

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
	assert.Equal(t, "https://hf-mirror.example", updated.Spec.Url)
	mockStorage.AssertExpectations(t)
}

func TestSyncWorkspaceModelRegistry_LeavesAMatchingRegistryAlone(t *testing.T) {
	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return([]v1.ModelRegistry{builtinRegistryRow(4, "https://huggingface.co")}, nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{Enabled: true})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// Provisioning must never adopt a registry a user created, even one sitting
// under the name the control plane would have used.
func TestSyncWorkspaceModelRegistry_NeverTouchesAUserRegistry(t *testing.T) {
	userOwned := builtinRegistryRow(4, "https://huggingface.co")
	userOwned.Metadata.Annotations = nil

	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{userOwned}, nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://elsewhere.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// A row already on its way out is left to the teardown that is under way.
func TestSyncWorkspaceModelRegistry_SkipsARegistryBeingDeleted(t *testing.T) {
	deleting := builtinRegistryRow(4, "https://huggingface.co")
	deleting.Metadata.DeletionTimestamp = time.Now().UTC().Format(time.RFC3339)

	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{deleting}, nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{
		Enabled:             true,
		HuggingFaceEndpoint: "https://elsewhere.example",
	})

	require.NoError(t, c.syncWorkspaceModelRegistry(workspaceNamed("default")))

	mockStorage.AssertNotCalled(t, "UpdateModelRegistry", mock.Anything, mock.Anything)
	mockStorage.AssertNotCalled(t, "CreateModelRegistry", mock.Anything)
}

// Credentials a user attached to the built-in registry are theirs; re-pointing
// the URL must not take them away.
func TestSyncWorkspaceModelRegistry_KeepsUserSuppliedCredentials(t *testing.T) {
	stored := builtinRegistryRow(4, "https://huggingface.co")
	stored.Spec.Credentials = "hf_token"

	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).Return([]v1.ModelRegistry{stored}, nil)

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

	mockStorage := &storagemocks.MockStorage{}
	mockStorage.On("ListModelRegistry", mock.Anything).
		Return([]v1.ModelRegistry{builtinRegistryRow(4, "https://huggingface.co"), userOwned}, nil)
	mockStorage.On("DeleteModelRegistry", "4").Return(nil)

	c := newBuiltinRegistryController(mockStorage, model_registry.BuiltinConfig{Enabled: true})

	workspace := workspaceNamed("default")
	require.NoError(t, c.DeleteWorkspaceModelRegistry(&workspace))

	// Only the provisioned one. A user's registry is theirs, and workspace
	// deletion is already refused while any remain.
	mockStorage.AssertNotCalled(t, "DeleteModelRegistry", "9")
	mockStorage.AssertExpectations(t)
}
