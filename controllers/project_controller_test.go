package controllers

import (
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/neutree-ai/neutree/pkg/scheme"
)

type projectObjectStorage struct {
	updatedID string
	updated   scheme.Object
}

func (s *projectObjectStorage) UpdateMetadata(string, scheme.Object) error { return nil }
func (s *projectObjectStorage) UpdateSpec(string, scheme.Object) error     { return nil }
func (s *projectObjectStorage) UpdateStatus(id string, obj scheme.Object) error {
	s.updatedID, s.updated = id, obj
	return nil
}
func (s *projectObjectStorage) Get(string, scheme.Object) error { return nil }
func (s *projectObjectStorage) List(scheme.ObjectList, storage.ListOption) error { return nil }

func TestProjectControllerInitializesMissingStatus(t *testing.T) {
	store := &projectObjectStorage{}
	controller, err := NewProjectController(&ProjectControllerOption{Storage: store})
	require.NoError(t, err)

	err = controller.Reconcile(&v1.Project{ID: "project-1", Name: "Default"})
	require.NoError(t, err)
	require.Equal(t, "project-1", store.updatedID)
	require.Equal(t, v1.ProjectStatusEnabled, store.updated.(*v1.Project).Status)
}

func TestProjectControllerLeavesKnownStatusesAlone(t *testing.T) {
	store := &projectObjectStorage{}
	controller, err := NewProjectController(&ProjectControllerOption{Storage: store})
	require.NoError(t, err)

	for _, status := range []string{v1.ProjectStatusEnabled, v1.ProjectStatusDisabled} {
		require.NoError(t, controller.Reconcile(&v1.Project{Status: status}))
	}
	require.Empty(t, store.updatedID)
}

func TestProjectControllerRejectsInvalidStatusAndType(t *testing.T) {
	controller, err := NewProjectController(&ProjectControllerOption{Storage: &projectObjectStorage{}})
	require.NoError(t, err)
	require.Error(t, controller.Reconcile(&v1.Project{ID: "project-1", Name: "p", Status: "unknown"}))
	require.Error(t, controller.Reconcile(&v1.ApiKey{}))
}

func TestNewProjectControllerRequiresStorage(t *testing.T) {
	controller, err := NewProjectController(nil)
	require.Error(t, err)
	require.Nil(t, controller)
}
