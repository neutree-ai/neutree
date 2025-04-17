package controllers

import (
	"context"
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type EngineController struct {
	baseController *BaseController

	storage     storage.Storage
	syncHandler func(engine *v1.Engine) error // Added syncHandler field
}

type EngineControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewEngineController(option *EngineControllerOption) (*EngineController, error) {
	c := &EngineController{
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "engine"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *EngineController) Start(ctx context.Context) {
	klog.Infof("Starting engine controller")

	c.baseController.Start(ctx, c, c)
}

func (c *EngineController) Reconcile(key interface{}) error {
	_engineID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to engineID")
	}

	engineID := strconv.Itoa(_engineID)

	obj, err := c.storage.GetEngine(engineID)
	if err != nil {
		return errors.Wrapf(err, "failed to get engine %s", engineID)
	}

	klog.V(4).Info("Reconcile engine " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *EngineController) ListKeys() ([]interface{}, error) {
	engines, err := c.storage.ListEngine(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(engines))
	for i := range engines {
		keys[i] = engines[i].ID
	}

	return keys, nil
}

func (c *EngineController) sync(obj *v1.Engine) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && (obj.Status.Phase == v1.EnginePhaseDeleted || obj.Status.Phase == v1.EnginePhaseFailed) {
			klog.Infof("Engine %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteEngine(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete engine in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting engine " + obj.Metadata.Name)
		// Update status to DELETED
		err = c.updateStatus(obj, v1.EnginePhaseDeleted, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update engine %s status to DELETED", obj.Metadata.Name)
		}

		return nil
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.EnginePhasePending {
		klog.Infof("Engine %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.EnginePhaseCreated, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update engine %s status to CREATED", obj.Metadata.Name)
		}

		return nil
	}

	return nil
}

func (c *EngineController) updateStatus(obj *v1.Engine, phase v1.EnginePhase, err error) error {
	newStatus := &v1.EngineStatus{
		LastTransitionTime: func() *time.Time {
			now := time.Now()
			return &now
		}(),
		Phase: phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	return c.storage.UpdateEngine(strconv.Itoa(obj.ID), &v1.Engine{Status: newStatus})
}
