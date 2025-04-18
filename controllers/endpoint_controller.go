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

type EndpointController struct {
	baseController *BaseController

	storage     storage.Storage
	syncHandler func(endpoint *v1.Endpoint) error // Added syncHandler field
}

type EndpointControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewEndpointController(option *EndpointControllerOption) (*EndpointController, error) {
	c := &EndpointController{
		baseController: &BaseController{
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "endpoint"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *EndpointController) Start(ctx context.Context) {
	klog.Infof("Starting endpoint controller")

	c.baseController.Start(ctx, c, c)
}

func (c *EndpointController) Reconcile(key interface{}) error {
	_endpointID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to endpointID")
	}

	endpointID := strconv.Itoa(_endpointID)

	obj, err := c.storage.GetEndpoint(endpointID)
	if err != nil {
		return errors.Wrapf(err, "failed to get endpoint %s", endpointID)
	}

	klog.V(4).Info("Reconcile endpoint " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *EndpointController) ListKeys() ([]interface{}, error) {
	endpoints, err := c.storage.ListEndpoint(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(endpoints))
	for i := range endpoints {
		keys[i] = endpoints[i].ID
	}

	return keys, nil
}

func (c *EndpointController) sync(obj *v1.Endpoint) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.EndpointPhaseDELETED {
			klog.Infof("Endpoint %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteEndpoint(strconv.Itoa(obj.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete endpoint in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting endpoint " + obj.Metadata.Name)
		// Update status to DELETED
		err = c.updateStatus(obj, v1.EndpointPhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status to DELETED", obj.Metadata.Name)
		}

		return nil
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to RUNNING.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.EndpointPhasePENDING {
		klog.Infof("Endpoint %s is PENDING or has no status, updating to RUNNING", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.EndpointPhaseRUNNING, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update endpoint %s status to RUNNING", obj.Metadata.Name)
		}

		return nil
	}

	return nil
}

func (c *EndpointController) updateStatus(obj *v1.Endpoint, phase v1.EndpointPhase, err error) error {
	newStatus := &v1.EndpointStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	return c.storage.UpdateEndpoint(strconv.Itoa(obj.ID), &v1.Endpoint{Status: newStatus})
}
