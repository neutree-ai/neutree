package controllers

import (
	"strconv"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ModelRegistryController struct {
	storage storage.Storage

	syncHandler func(modelRegistry *v1.ModelRegistry) error
}

type ModelRegistryControllerOption struct {
	Storage storage.Storage
}

func NewModelRegistryController(option *ModelRegistryControllerOption) (*ModelRegistryController, error) {
	c := &ModelRegistryController{
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ModelRegistryController) Reconcile(obj interface{}) error {
	modelRegistry, ok := obj.(*v1.ModelRegistry)
	if !ok {
		return errors.New("failed to assert obj to *v1.ModelRegistry")
	}

	klog.V(4).Info("Reconcile model registry " + modelRegistry.Metadata.Name)

	return c.syncHandler(modelRegistry)
}

func (c *ModelRegistryController) sync(obj *v1.ModelRegistry) (err error) {
	var (
		modelRegistry model_registry.ModelRegistry
	)

	// Handle deletion early - bypass defer block for already-deleted resources
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		isForceDelete := IsForceDelete(obj.Metadata.Annotations)

		if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseDELETED {
			klog.Info("Model registry " + obj.Metadata.Name + " is already deleted, delete resource from storage")

			err = c.storage.DeleteModelRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete model registry %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Infof("Deleting model registry %s (force=%v)", obj.Metadata.Name, isForceDelete)

		// For deletion, we need to track if it succeeds to set correct phase
		deleteErr := func() error {
			modelRegistry, err = model_registry.NewModelRegistry(obj)
			if err == nil {
				// only disconnect model registry when it config is correct.
				if err = modelRegistry.Disconnect(); err != nil {
					return errors.Wrapf(err, "failed to disconnect model registry %s/%s",
						obj.Metadata.Workspace, obj.Metadata.Name)
				}
			}

			return nil
		}()

		// Update status to DELETED if successful, or FAILED if not
		// For force delete, always mark as DELETED even if there were errors
		phase := v1.ModelRegistryPhaseDELETED
		if deleteErr != nil && !isForceDelete {
			phase = v1.ModelRegistryPhaseFAILED
		}

		updateErr := c.updateStatus(obj, phase, deleteErr)
		if updateErr != nil {
			klog.Errorf("failed to update model registry %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
		}

		LogForceDeletionWarning(isForceDelete, "model registry", obj.Metadata.Workspace, obj.Metadata.Name, deleteErr)

		klog.Info("Model registry " + obj.Metadata.Name + " deletion processed")

		// Return the original delete error if any, unless it's a force delete
		if deleteErr != nil && !isForceDelete {
			return deleteErr
		}

		return nil
	}

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		// Determine phase based on error
		phase := v1.ModelRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ModelRegistryPhaseFAILED
		}

		// Skip update if already in correct phase and no error change
		if obj.Status != nil && obj.Status.Phase == phase &&
			(err != nil) == (obj.Status.ErrorMessage != "") {
			return
		}

		updateErr := c.updateStatus(obj, phase, err)
		if updateErr != nil {
			klog.Errorf("failed to update model registry %s/%s status: %v",
				obj.Metadata.Workspace, obj.Metadata.Name, updateErr)
		}
	}()

	modelRegistry, err = model_registry.NewModelRegistry(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to create model registry %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.ModelRegistryPhasePENDING {
		err = modelRegistry.Connect()
		if err != nil {
			return errors.Wrapf(err, "failed to connect model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseFAILED {
		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrapf(err, "failed to disconnect model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		if err = modelRegistry.Connect(); err != nil {
			return errors.Wrapf(err, "failed to connect model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseCONNECTED {
		if err = modelRegistry.HealthyCheck(); err != nil {
			return errors.Wrapf(err, "health check failed for model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}
	}

	return nil
}

func (c *ModelRegistryController) updateStatus(obj *v1.ModelRegistry, phase v1.ModelRegistryPhase, err error) error {
	newStatus := &v1.ModelRegistryStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
	}

	return c.storage.UpdateModelRegistry(strconv.Itoa(obj.ID), &v1.ModelRegistry{Status: newStatus})
}
