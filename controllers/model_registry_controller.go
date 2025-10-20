package controllers

import (
	"strconv"
	"time"

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

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseDELETED {
			klog.Info("Model registry " + obj.Metadata.Name + " is already deleted, delete resource from storage")

			err = c.storage.DeleteModelRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrap(err, "failed to delete model registry "+obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting model registry " + obj.Metadata.Name)

		modelRegistry, err = model_registry.NewModelRegistry(obj)
		if err == nil {
			// only disconnect model registry when it config is correct.
			if err = modelRegistry.Disconnect(); err != nil {
				return errors.Wrap(err, "failed to disconnect model registry "+obj.Metadata.Name)
			}
		}

		klog.Info("Model registry " + obj.Metadata.Name + " is deleted")

		if err = c.updateStatus(obj, v1.ModelRegistryPhaseDELETED, nil); err != nil {
			klog.Errorf("failed to update model registry %s, err: %v", obj.Metadata.Name, err)
		}

		return nil
	}

	defer func() {
		phase := v1.ModelRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ModelRegistryPhaseFAILED
		}

		if obj.Status != nil && obj.Status.Phase == phase {
			return
		}

		updateStatusErr := c.updateStatus(obj, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update model registry %s status, err: %v ", obj.Metadata.Name, updateStatusErr)
		}
	}()

	modelRegistry, err = model_registry.NewModelRegistry(obj)
	if err != nil {
		return errors.Wrap(err, "failed to create model registry "+obj.Metadata.Name)
	}

	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.ModelRegistryPhasePENDING {
		err = modelRegistry.Connect()
		if err != nil {
			return errors.Wrap(err, "failed to connect model registry "+obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseFAILED {
		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrap(err, "failed to disconnect model registry "+obj.Metadata.Name)
		}

		if err = modelRegistry.Connect(); err != nil {
			return errors.Wrap(err, "failed to connect model registry "+obj.Metadata.Name)
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
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	}

	return c.storage.UpdateModelRegistry(strconv.Itoa(obj.ID), &v1.ModelRegistry{Status: newStatus})
}
