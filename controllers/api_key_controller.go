package controllers

import (
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ApiKeyController struct {
	storage     storage.Storage
	syncHandler func(apiKey *v1.ApiKey) error // Added syncHandler field

	gw gateway.Gateway
}

type ApiKeyControllerOption struct {
	Storage storage.Storage
	Gw      gateway.Gateway
}

func NewApiKeyController(option *ApiKeyControllerOption) (*ApiKeyController, error) {
	c := &ApiKeyController{
		storage: option.Storage,
		gw:      option.Gw,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ApiKeyController) Reconcile(obj interface{}) error {
	apiKey, ok := obj.(*v1.ApiKey)
	if !ok {
		return errors.New("failed to assert obj to *v1.ApiKey")
	}

	klog.V(4).Info("Reconcile api_key " + apiKey.Metadata.Name)

	return c.syncHandler(apiKey)
}

func (c *ApiKeyController) sync(obj *v1.ApiKey) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.ApiKeyPhaseDELETED {
			klog.Infof("ApiKey %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteApiKey(obj.ID)
			if err != nil {
				return errors.Wrapf(err, "failed to delete api_key %s/%s from DB",
					obj.Metadata.Workspace, obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting api_key " + obj.Metadata.Name)

		err = c.gw.DeleteAPIKey(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to delete api_key %s/%s from gateway",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		// Update status to DELETED
		err = c.updateStatus(obj, v1.ApiKeyPhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update api_key %s/%s status to DELETED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	// sync api key when not deleting
	err = c.gw.SyncAPIKey(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to sync api_key %s/%s to gateway",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.ApiKeyPhasePENDING {
		klog.Infof("ApiKey %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.ApiKeyPhaseCREATED, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update api_key %s/%s status to CREATED",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	return nil
}

func (c *ApiKeyController) updateStatus(obj *v1.ApiKey, phase v1.ApiKeyPhase, err error) error {
	newStatus := &v1.ApiKeyStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339Nano),
		Phase:              phase,
		SkValue:            obj.Status.SkValue,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	return c.storage.UpdateApiKey(obj.ID, &v1.ApiKey{Status: newStatus})
}
