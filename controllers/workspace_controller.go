package controllers

import (
	"context"
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type WorkspaceController struct {
	storage     storage.Storage
	syncHandler func(workspace *v1.Workspace) error // Added syncHandler field

	acceleratorManager accelerator.Manager
}

type WorkspaceControllerOption struct {
	Storage            storage.Storage
	AcceleratorManager accelerator.Manager
}

func NewWorkspaceController(option *WorkspaceControllerOption) (*WorkspaceController, error) {
	c := &WorkspaceController{
		storage:            option.Storage,
		acceleratorManager: option.AcceleratorManager,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *WorkspaceController) Reconcile(obj interface{}) error {
	workspace, ok := obj.(*v1.Workspace)
	if !ok {
		return errors.New("failed to assert obj to *v1.Workspace")
	}

	klog.V(4).Info("Reconcile workspace " + workspace.Metadata.Name)

	return c.syncHandler(workspace)
}

func (c *WorkspaceController) sync(obj *v1.Workspace) error {
	var err error
	workspaceIDStr := strconv.Itoa(obj.ID)

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.WorkspacePhaseDELETED {
			klog.Infof("Workspace %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteWorkspace(workspaceIDStr)
			if err != nil {
				// Don't wrap ErrResourceNotFound, as it means already deleted.
				if errors.Is(err, storage.ErrResourceNotFound) {
					klog.Warningf("Workspace %s not found during deletion, assuming already deleted", obj.Metadata.Name)
					return nil
				}

				return errors.Wrapf(err, "failed to delete workspace in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting workspace " + obj.Metadata.Name)

		err = c.DeleteWorkspaceEngine(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to delete workspace engine %s", obj.Metadata.Name)
		}

		// Update status to DELETED
		err = c.updateStatus(obj, v1.WorkspacePhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update workspace %s status to DELETED", obj.Metadata.Name)
		}

		return nil
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.WorkspacePhasePENDING {
		klog.Infof("Workspace %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.WorkspacePhaseCREATED, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update workspace %s status to CREATED", obj.Metadata.Name)
		}

		return nil
	}
	// If status is CREATED, sync the workspace engine.
	if obj.Status.Phase == v1.WorkspacePhaseCREATED {
		err = c.syncWorkspaceEngine(*obj)
		if err != nil {
			return errors.Wrapf(err, "failed to sync workspace %s engine", obj.Metadata.Name)
		}
	}

	// If already CREATED or DELETED (without deletion timestamp), do nothing.
	if obj.Status.Phase == v1.WorkspacePhaseCREATED || obj.Status.Phase == v1.WorkspacePhaseDELETED {
		klog.V(4).Infof("Workspace %s is already in phase %s, no action needed", obj.Metadata.Name, obj.Status.Phase)
		return nil
	}

	klog.Warningf("Workspace %s is in an unexpected phase %s", obj.Metadata.Name, obj.Status.Phase)

	return nil
}

func (c *WorkspaceController) updateStatus(obj *v1.Workspace, phase v1.WorkspacePhase, err error) error {
	newStatus := &v1.WorkspaceStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	// Preserve existing fields if needed, e.g., ServiceURL
	if obj.Status != nil {
		newStatus.ServiceURL = obj.Status.ServiceURL
	}

	// Avoid unnecessary updates if status hasn't changed meaningfully
	// (simple check for phase and error message presence)
	if obj.Status != nil && obj.Status.Phase == newStatus.Phase &&
		(obj.Status.ErrorMessage != "") == (newStatus.ErrorMessage != "") {
		klog.V(4).Infof("Skipping status update for workspace %s, phase %s is already set", obj.Metadata.Name, phase)
		return nil
	}

	workspaceIDStr := strconv.Itoa(obj.ID)

	return c.storage.UpdateWorkspace(workspaceIDStr, &v1.Workspace{Status: newStatus})
}

func (c *WorkspaceController) syncWorkspaceEngine(workspace v1.Workspace) error {
	engines, err := c.acceleratorManager.GetAllAcceleratorSupportEngines(context.Background())
	if err != nil {
		return errors.Wrapf(err, "failed to get accelerator supported engines for workspace %s", workspace.Metadata.Name)
	}

	// set workspace name to engine metadata
	for i := range engines {
		engines[i].Metadata.Workspace = workspace.Metadata.Name
	}

	for _, engine := range engines {
		if err := c.createOrUpdateEngine(engine); err != nil {
			return errors.Wrapf(err, "failed to create or update engine %s for workspace %s",
				engine.Metadata.Name, workspace.Metadata.Name)
		}
	}

	return nil
}

func (c *WorkspaceController) createOrUpdateEngine(engine *v1.Engine) error {
	engines, err := c.storage.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(engine.Metadata.Name),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(engine.Metadata.Workspace),
			},
		},
	})

	if err != nil {
		return errors.Wrapf(err, "failed to list engines for workspace %s", engine.Metadata.Workspace)
	}

	if len(engines) == 0 {
		if err := c.storage.CreateEngine(engine); err != nil {
			return errors.Wrapf(err, "failed to create engine %s/%s",
				engine.Metadata.Workspace, engine.Metadata.Name)
		}
		return nil
	}

	result, diff, err := util.JsonEqual(engines[0].Spec, engine.Spec)
	if err != nil {
		return errors.Wrapf(err, "failed to compare engine spec for %s/%s",
			engine.Metadata.Workspace, engine.Metadata.Name)
	}

	// If the specs are equal, no need to update
	if result {
		return nil
	}

	klog.V(4).Infof("Engine %s spec has changed, diff: %s", engine.Metadata.Name, diff)

	engines[0].Spec = engine.Spec

	if err := c.storage.UpdateEngine(strconv.Itoa(engines[0].ID), &engines[0]); err != nil {
		return errors.Wrapf(err, "failed to update engine %s/%s",
			engine.Metadata.Workspace, engine.Metadata.Name)
	}
	return nil
}

func (c *WorkspaceController) DeleteWorkspaceEngine(workspace *v1.Workspace) error {
	engines, err := c.storage.ListEngine(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace.Metadata.Name),
			},
		},
	})
	if err != nil {
		return errors.Wrapf(err, "failed to list engines for workspace %s", workspace.Metadata.Name)
	}

	for _, engine := range engines {
		if err = c.storage.DeleteEngine(strconv.Itoa(engine.ID)); err != nil {
			return errors.Wrapf(err, "failed to delete engine %s/%s",
				workspace.Metadata.Name, engine.Metadata.Name)
		}
	}

	return nil
}
