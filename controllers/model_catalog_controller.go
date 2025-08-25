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

type ModelCatalogController struct {
	baseController *BaseController

	storage storage.Storage

	syncHandler func(modelCatalog *v1.ModelCatalog) error
}

type ModelCatalogControllerOption struct {
	Storage storage.Storage
	Workers int
}

func NewModelCatalogController(opt *ModelCatalogControllerOption) (*ModelCatalogController, error) {
	c := &ModelCatalogController{
		baseController: &BaseController{
			//nolint:staticcheck
			queue: workqueue.NewRateLimitingQueueWithConfig(workqueue.DefaultControllerRateLimiter(),
				workqueue.RateLimitingQueueConfig{Name: "modelcatalog"}),
			workers:      opt.Workers,
			syncInterval: time.Second * 10,
		},
		storage: opt.Storage,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ModelCatalogController) Start(ctx context.Context) {
	klog.Infof("Starting model catalog controller")
	c.baseController.Start(ctx, c, c)
}

func (c *ModelCatalogController) ListKeys() ([]interface{}, error) {
	modelCatalogs, err := c.storage.ListModelCatalog(storage.ListOption{})
	if err != nil {
		return nil, err
	}

	keys := make([]interface{}, len(modelCatalogs))
	for i := range modelCatalogs {
		keys[i] = modelCatalogs[i].ID
	}

	return keys, nil
}

func (c *ModelCatalogController) Reconcile(key interface{}) error {
	modelCatalogID, ok := key.(int)
	if !ok {
		return errors.New("failed to assert key to modelCatalogID")
	}

	obj, err := c.storage.GetModelCatalog(strconv.Itoa(modelCatalogID))
	if err != nil {
		return errors.Wrapf(err, "failed to get model catalog %s", strconv.Itoa(modelCatalogID))
	}

	klog.V(4).Info("Reconciling model catalog " + obj.Metadata.Name)

	return c.syncHandler(obj)
}

func (c *ModelCatalogController) sync(modelCatalog *v1.ModelCatalog) error {
	klog.V(4).Infof("Syncing model catalog %s/%s", modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name)

	// Handle deletion
	if modelCatalog.Metadata != nil && modelCatalog.Metadata.DeletionTimestamp != "" {
		if modelCatalog.Status != nil && (modelCatalog.Status.Phase == v1.ModelCatalogPhaseDELETED || modelCatalog.Status.Phase == v1.ModelCatalogPhaseFAILED) {
			klog.Infof("Model catalog %s already marked as deleted, removing from DB", modelCatalog.Metadata.Name)

			err := c.storage.DeleteModelCatalog(strconv.Itoa(modelCatalog.ID))
			if err != nil {
				return errors.Wrapf(err, "failed to delete model catalog in DB %s", modelCatalog.Metadata.Name)
			}

			return nil
		}

		klog.Info("Deleting model catalog " + modelCatalog.Metadata.Name)
		// Update status to DELETED
		err := c.updateStatus(modelCatalog, v1.ModelCatalogPhaseDELETED, nil)
		if err != nil {
			return errors.Wrapf(err, "failed to update model catalog %s status to DELETED", modelCatalog.Metadata.Name)
		}

		return nil
	}

	// Set default values if not provided
	if modelCatalog.APIVersion == "" {
		modelCatalog.APIVersion = "v1"
	}

	if modelCatalog.Kind == "" {
		modelCatalog.Kind = "ModelCatalog"
	}

	// Initialize status if nil
	if modelCatalog.Status == nil {
		modelCatalog.Status = &v1.ModelCatalogStatus{
			Phase: v1.ModelCatalogPhasePENDING,
		}
	}

	// Process model catalog based on current phase
	switch modelCatalog.Status.Phase {
	case v1.ModelCatalogPhasePENDING:
		return c.processPendingModelCatalog(modelCatalog)
	case v1.ModelCatalogPhaseREADY:
		return c.processReadyModelCatalog(modelCatalog)
	case v1.ModelCatalogPhaseFAILED:
		return c.processFailedModelCatalog(modelCatalog)
	default:
		klog.V(4).Infof("Model catalog %s/%s is in phase %s, no action needed",
			modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name, modelCatalog.Status.Phase)
	}

	return nil
}

func (c *ModelCatalogController) processPendingModelCatalog(modelCatalog *v1.ModelCatalog) error {
	klog.V(4).Infof("Processing pending model catalog %s/%s",
		modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name)

	// Set default values for spec if not provided
	if modelCatalog.Spec.Resources == nil {
		modelCatalog.Spec.Resources = &v1.ResourceSpec{}
	}

	if modelCatalog.Spec.Replicas == nil {
		modelCatalog.Spec.Replicas = &v1.ReplicaSpec{
			Num: intPtr(1),
		}
	}

	if modelCatalog.Spec.DeploymentOptions == nil {
		modelCatalog.Spec.DeploymentOptions = make(map[string]interface{})
	}

	if modelCatalog.Spec.Variables == nil {
		modelCatalog.Spec.Variables = make(map[string]interface{})
	}

	// Update status to ready
	modelCatalog.Status.Phase = v1.ModelCatalogPhaseREADY
	modelCatalog.Status.ErrorMessage = ""
	modelCatalog.Status.LastTransitionTime = time.Now().UTC().Format(time.RFC3339)

	if err := c.storage.UpdateModelCatalog(strconv.Itoa(modelCatalog.ID), modelCatalog); err != nil {
		return errors.Wrapf(err, "failed to update model catalog status to ready")
	}

	klog.Infof("Model catalog %s/%s is ready", modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name)

	return nil
}

func (c *ModelCatalogController) processReadyModelCatalog(modelCatalog *v1.ModelCatalog) error {
	klog.V(4).Infof("Processing ready model catalog %s/%s",
		modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name)

	// For ready model catalogs, we just need to ensure they remain valid
	// Additional business logic can be added here as needed

	return nil
}

func (c *ModelCatalogController) processFailedModelCatalog(modelCatalog *v1.ModelCatalog) error {
	klog.V(4).Infof("Processing failed model catalog %s/%s",
		modelCatalog.Metadata.Workspace, modelCatalog.Metadata.Name)

	// If validation passes, move back to pending to retry processing
	modelCatalog.Status.Phase = v1.ModelCatalogPhasePENDING
	modelCatalog.Status.ErrorMessage = ""
	modelCatalog.Status.LastTransitionTime = time.Now().UTC().Format(time.RFC3339)

	if err := c.storage.UpdateModelCatalog(strconv.Itoa(modelCatalog.ID), modelCatalog); err != nil {
		return errors.Wrapf(err, "failed to update model catalog status to pending")
	}

	return nil
}

// Helper function to create int pointer
func intPtr(i int) *int {
	return &i
}

func (c *ModelCatalogController) updateStatus(obj *v1.ModelCatalog, phase v1.ModelCatalogPhase, err error) error {
	newStatus := &v1.ModelCatalogStatus{
		LastTransitionTime: time.Now().Format(time.RFC3339),
		Phase:              phase,
	}
	if err != nil {
		newStatus.ErrorMessage = err.Error()
	} else {
		newStatus.ErrorMessage = ""
	}

	return c.storage.UpdateModelCatalog(strconv.Itoa(obj.ID), &v1.ModelCatalog{Status: newStatus})
}
