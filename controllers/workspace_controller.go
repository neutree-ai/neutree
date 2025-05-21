package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type WorkspaceController struct {
	baseController *BaseController

	storage     storage.Storage
	syncHandler func(workspace *v1.Workspace) error // Added syncHandler field
}

type WorkspaceControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewWorkspaceController(option *WorkspaceControllerOption) (*WorkspaceController, error) {
	c := &WorkspaceController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
				workqueue.RateLimitingQueueConfig{Name: "workspace"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *WorkspaceController) Start(ctx context.Context) {
	klog.Infof("Starting workspace controller")

	c.baseController.Start(ctx, c, c)
}

func (c *WorkspaceController) Reconcile(key interface{}) error {
	_workspaceID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to workspaceID")
	}

	workspaceID := strconv.Itoa(_workspaceID)

	obj, err := c.storage.GetWorkspace(workspaceID)
	if err != nil {
		// Let the sync loop handle ErrResourceNotFound if necessary, or retry on other errors.
		return errors.Wrapf(err, "failed to get workspace %s", workspaceID)
	}

	klog.V(4).Info("Reconcile workspace " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *WorkspaceController) ListKeys() ([]interface{}, error) {
	workspaces, err := c.storage.ListWorkspace(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(workspaces))
	for i := range workspaces {
		keys[i] = workspaces[i].ID
	}

	return keys, nil
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
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}

	// Preserve existing fields if needed, e.g., ServiceURL
	if obj.Status != nil {
		newStatus.ServiceURL = obj.Status.ServiceURL
	}

	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
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
	llamaCppV1Engine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name:      "llama-cpp",
			Workspace: workspace.Metadata.Name,
		},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v1",
					ValuesSchema: map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
						"properties": map[string]interface{}{
							"n_threads": map[string]interface{}{
								"type": "number",
							},
						},
					},
				},
			},
			SupportedTasks: []string{"text-generation", "text-embedding"},
		},
	}

	vllmV1Engine := &v1.Engine{
		APIVersion: "v1",
		Kind:       "Engine",
		Metadata: &v1.Metadata{
			Name:      "vllm",
			Workspace: workspace.Metadata.Name,
		},
		Spec: &v1.EngineSpec{
			Versions: []*v1.EngineVersion{
				{
					Version: "v1",
					ValuesSchema: map[string]interface{}{
						"$schema": "http://json-schema.org/draft-07/schema#",
						"type":    "object",
						"properties": map[string]interface{}{
							"dtype": map[string]interface{}{
								"type": "string",
							},
							"gpu_memory_utilization": map[string]interface{}{
								"type": "number",
							},
						},
					},
				},
			},
			SupportedTasks: []string{"text-generation"},
		},
	}

	engines := []*v1.Engine{
		llamaCppV1Engine,
		vllmV1Engine,
	}

	for _, engine := range engines {
		if err := c.createOrUpdateEngine(engine); err != nil {
			return err
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
		return err
	}

	if len(engines) == 0 {
		return c.storage.CreateEngine(engine)
	}

	result, diff, err := util.JsonEqual(engines[0].Spec, engine.Spec)
	if err != nil {
		return errors.Wrapf(err, "failed to compare engine spec")
	}

	// If the specs are equal, no need to update
	if result {
		return nil
	}

	klog.V(4).Infof("Engine %s spec has changed, diff: %s", engine.Metadata.Name, diff)

	engines[0].Spec = engine.Spec

	return c.storage.UpdateEngine(strconv.Itoa(engines[0].ID), &engines[0])
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
		return err
	}

	for _, engine := range engines {
		err = c.storage.DeleteEngine(strconv.Itoa(engine.ID))
		if err != nil {
			return err
		}
	}

	return nil
}
