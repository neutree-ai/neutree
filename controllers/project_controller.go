package controllers

import (
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ProjectController persists Project lifecycle state. Project deletion and
// APIKey association checks remain database-owned so they are transactional.
type ProjectController struct {
	storage storage.ObjectStorage
}

type ProjectControllerOption struct {
	Storage storage.ObjectStorage
}

func NewProjectController(option *ProjectControllerOption) (*ProjectController, error) {
	if option == nil || option.Storage == nil {
		return nil, errors.New("project controller option is required")
	}
	return &ProjectController{storage: option.Storage}, nil
}

func (c *ProjectController) Reconcile(obj interface{}) error {
	project, ok := obj.(*v1.Project)
	if !ok {
		return errors.New("failed to assert obj to *v1.Project")
	}
	if project.Status == "" {
		klog.Infof("Project %s has no status, marking it enabled", project.Name)
		return c.storage.UpdateStatus(project.ID, &v1.Project{Status: v1.ProjectStatusEnabled})
	}
	if project.Status != v1.ProjectStatusEnabled && project.Status != v1.ProjectStatusDisabled {
		return errors.Errorf("project %s has invalid status %q", project.Name, project.Status)
	}
	return nil
}
