package controllers

import (
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type EngineController struct {
	storage     storage.Storage
	syncHandler func(engine *v1.Engine) error // Added syncHandler field
}

type EngineControllerOption struct {
	Storage storage.Storage
}

func NewEngineController(option *EngineControllerOption) (*EngineController, error) {
	c := &EngineController{
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *EngineController) Reconcile(obj interface{}) error {
	engine, ok := obj.(*v1.Engine)
	if !ok {
		return errors.New("failed to assert obj to *v1.Engine")
	}

	klog.V(4).Info("Reconcile engine " + engine.Metadata.Name)

	return c.syncHandler(engine)
}

func (c *EngineController) sync(obj *v1.Engine) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && (obj.Status.Phase == v1.EnginePhaseDeleted || obj.Status.Phase == v1.EnginePhaseFailed) {
			klog.Infof("Engine %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteEngine(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete engine %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting engine " + obj.Metadata.Name)
		// Update status to DELETED
		err = c.updateStatus(obj, v1.EnginePhaseDeleted, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update engine %s/%s status to DELETED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.EnginePhasePending {
		klog.Infof("Engine %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.EnginePhaseCreated, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update engine %s/%s status to CREATED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	return nil
}

func (c *EngineController) updateStatus(obj *v1.Engine, phase v1.EnginePhase, err error) error {
	newStatus := &v1.EngineStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	return c.storage.UpdateEngine(strconv.Itoa(obj.ID), &v1.Engine{Status: newStatus})
}
