package controllers

import (
	"context"
	"time"

	"github.com/pkg/errors"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ApiKeyController struct {
	baseController *BaseController

	storage     storage.Storage
	syncHandler func(apiKey *v1.ApiKey) error // Added syncHandler field

	gw gateway.Gateway
}

type ApiKeyControllerOption struct {
	Storage storage.Storage
	Gw      gateway.Gateway
	Workers int
}

func NewApiKeyController(option *ApiKeyControllerOption) (*ApiKeyController, error) {
	c := &ApiKeyController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue:        workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(), workqueue.RateLimitingQueueConfig{Name: "api_key"}),
			workers:      option.Workers,
			syncInterval: time.Second * 10,
		},
		storage: option.Storage,
		gw:      option.Gw,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ApiKeyController) Start(ctx context.Context) {
	klog.Infof("Starting api_key controller")

	c.baseController.Start(ctx, c, c)
}

func (c *ApiKeyController) Reconcile(key interface{}) error {
	apiKeyID, ok := key.(string)
	if !ok {
		return errors.New("failed to assert key to apiKeyID")
	}

	obj, err := c.storage.GetApiKey(apiKeyID)
	if err != nil {
		return errors.Wrapf(err, "failed to get api_key %s", apiKeyID)
	}

	klog.V(4).Info("Reconcile api_key " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *ApiKeyController) ListKeys() ([]interface{}, error) {
	apiKeys, err := c.storage.ListApiKey(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(apiKeys))
	for i := range apiKeys {
		keys[i] = apiKeys[i].ID
	}

	return keys, nil
}

func (c *ApiKeyController) sync(obj *v1.ApiKey) error {
	var err error

	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		if obj.Status != nil && obj.Status.Phase == v1.ApiKeyPhaseDELETED {
			klog.Infof("ApiKey %s already marked as deleted, removing from DB", obj.Metadata.Name)

			err = c.storage.DeleteApiKey(obj.ID)
			if err != nil {
				return errors.Wrapf(err, "failed to delete api_key in DB %s", obj.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting api_key " + obj.Metadata.Name)

		err = c.gw.DeleteAPIKey(obj)
		if err != nil {
			return errors.Wrapf(err, "failed to delete api_key in gateway %s", obj.Metadata.Name)
		}

		// Update status to DELETED
		err = c.updateStatus(obj, v1.ApiKeyPhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update api_key %s status to DELETED", obj.Metadata.Name)
		}

		return nil
	}

	// sync api key when not deleting
	err = c.gw.SyncAPIKey(obj)
	if err != nil {
		return errors.Wrapf(err, "failed to sync api_key %s in gateway", obj.Metadata.Name)
	}

	// Handle creation/update (when not deleting)
	// If status is missing or PENDING, update it to CREATED.
	if obj.Status == nil || obj.Status.Phase == "" || obj.Status.Phase == v1.ApiKeyPhasePENDING {
		klog.Infof("ApiKey %s is PENDING or has no status, updating to CREATED", obj.Metadata.Name)
		err = c.updateStatus(obj, v1.ApiKeyPhaseCREATED, nil)

		if err != nil {
			return errors.Wrapf(err, "failed to update api_key %s status to CREATED", obj.Metadata.Name)
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
