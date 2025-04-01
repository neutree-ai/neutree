package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ModelRegistryController struct {
	baseController *BaseController

	storage storage.Storage
}

type ModelRegistryControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewModelRegistryController(option *ModelRegistryControllerOption) (*ModelRegistryController, error) {
	c := &ModelRegistryController{
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "model-registry"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	return c, nil
}

func (c *ModelRegistryController) Start(ctx context.Context) {
	klog.Infof("Starting model registry controller")

	c.baseController.Start(ctx, c, c)
}

func (c *ModelRegistryController) Reconcile(key interface{}) error {
	modelRegistryID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to modelRegistryID")
	}

	obj, err := c.storage.GetModelRegistry(strconv.Itoa(modelRegistryID))
	if err != nil {
		return errors.Wrapf(err, "failed to get model registry %s", strconv.Itoa(modelRegistryID))
	}

	klog.V(4).Info("Reconcile model registry " + obj.Metadata.Name)

	return c.sync(obj)
}

func (c *ModelRegistryController) ListKeys() ([]interface{}, error) {
	registries, err := c.storage.ListModelRegistry(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(registries))
	for i := range registries {
		keys[i] = registries[i].ID
	}

	return keys, nil
}

func (c *ModelRegistryController) sync(obj *v1.ModelRegistry) (err error) {
	modelRegistry, err := model_registry.New(obj)
	if err != nil {
		return err
	}

	if obj.Metadata.DeletionTimestamp != "" {
		if obj.Status.Phase == v1.ModelRegistryPhaseDELETED {
			klog.Info("Deleted model registry " + obj.Metadata.Name)

			err = c.storage.DeleteModelRegistry(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrap(err, "failed to delete model registry "+obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting model registry " + obj.Metadata.Name)

		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrap(err, "failed to disconnect model registry "+obj.Metadata.Name)
		}

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

		if obj.Status.Phase == phase {
			return
		}

		updateStatusErr := c.updateStatus(obj, phase, err)
		if updateStatusErr != nil {
			klog.Errorf("failed to update model registry %s status, err: %v ", obj.Metadata.Name, updateStatusErr)
		}
	}()

	if obj.Status.Phase == "" || obj.Status.Phase == v1.ModelRegistryPhasePENDING {
		klog.Info("Connect model registry " + obj.Metadata.Name)

		err = modelRegistry.Connect()
		if err != nil {
			return errors.Wrap(err, "failed to connect model registry "+obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status.Phase == v1.ModelRegistryPhaseFAILED {
		klog.Info("Reconnect model registry " + obj.Metadata.Name)

		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrap(err, "failed to disconnect model registry "+obj.Metadata.Name)
		}

		if err = modelRegistry.Connect(); err != nil {
			return errors.Wrap(err, "failed to connect model registry "+obj.Metadata.Name)
		}

		return nil
	}

	if obj.Status.Phase == v1.ModelRegistryPhaseCONNECTED {
		klog.Info("Health check model registry " + obj.Metadata.Name)

		healthy := modelRegistry.HealthyCheck()
		if !healthy {
			return errors.New("health check failed")
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
