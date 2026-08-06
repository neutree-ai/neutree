package controllers

import (
	"strconv"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ModelRegistryController struct {
	storage storage.Storage
	stats   model_registry.StatsAggregator

	syncHandler func(modelRegistry *v1.ModelRegistry) error
}

type ModelRegistryControllerOption struct {
	Storage storage.Storage
	// StatsStaleAfter overrides how long collected registry counters stay usable
	// before the tree is walked again. Zero uses the package default.
	StatsStaleAfter time.Duration
}

func NewModelRegistryController(option *ModelRegistryControllerOption) (*ModelRegistryController, error) {
	c := &ModelRegistryController{
		storage: option.Storage,
		stats:   model_registry.StatsAggregator{StaleAfter: option.StatsStaleAfter},
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
		// stats is what this reconcile will persist. It starts as whatever was
		// observed, so a reconcile that does not measure the registry still writes
		// the counters back — PostgREST replaces the whole status column.
		stats          = currentStats(obj)
		statsRefreshed bool
	)

	// Handle deletion early - bypass defer block for already-deleted resources
	if obj.Metadata != nil && obj.Metadata.DeletionTimestamp != "" {
		return c.syncDeletion(obj)
	}

	// Defer block to handle status updates for non-deletion paths
	defer func() {
		// Determine phase based on error
		phase := v1.ModelRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ModelRegistryPhaseFAILED
		}

		// Skip update if already in correct phase, no error change, and the
		// counters were not recomputed. The recomputation has to be written even
		// when the counters came out identical: its timestamp is what stops the
		// next reconcile, ten seconds later, from walking the model tree again.
		if !statsRefreshed && obj.Status != nil && obj.Status.Phase == phase &&
			(err != nil) == (obj.Status.ErrorMessage != "") {
			return
		}

		updateErr := c.updateStatus(obj, phase, err, stats)
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

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseFAILED {
		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrapf(err, "failed to disconnect model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}
	}

	if err = modelRegistry.Connect(); err != nil {
		return errors.Wrapf(err, "failed to connect model registry %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	if err = modelRegistry.HealthyCheck(); err != nil {
		return errors.Wrapf(err, "health check failed for model registry %s/%s",
			obj.Metadata.Workspace, obj.Metadata.Name)
	}

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseCONNECTED {
		// Refresh only after a registry has passed both connection and health checks.
		stats, statsRefreshed = c.stats.Refresh(modelRegistry, stats, obj.Metadata.WorkspaceName())
	}

	return nil
}

// syncDeletion drives a registry marked for deletion to its final state. It
// bypasses the status defer of the normal path: the phase here is decided by
// whether the disconnect worked, not by the reconcile's error.
func (c *ModelRegistryController) syncDeletion(obj *v1.ModelRegistry) error {
	isForceDelete := v1.IsForceDelete(obj.Metadata.Annotations)

	if obj.Status != nil && obj.Status.Phase == v1.ModelRegistryPhaseDELETED {
		klog.Info("Model registry " + obj.Metadata.Name + " is already deleted, delete resource from storage")

		if err := c.storage.DeleteModelRegistry(strconv.Itoa(obj.ID)); err != nil {
			return errors.Wrapf(err, "failed to delete model registry %s/%s from DB",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}

	klog.Infof("Deleting model registry %s (force=%v)", obj.Metadata.Name, isForceDelete)

	// For deletion, we need to track if it succeeds to set correct phase
	deleteErr := func() error {
		modelRegistry, err := model_registry.NewModelRegistry(obj)
		if err != nil {
			// only disconnect model registry when it config is correct.
			return nil
		}

		if err = modelRegistry.Disconnect(); err != nil {
			return errors.Wrapf(err, "failed to disconnect model registry %s/%s",
				obj.Metadata.Workspace, obj.Metadata.Name)
		}

		return nil
	}()

	// Update status to DELETED if successful, or FAILED if not
	// For force delete, always mark as DELETED even if there were errors
	phase := v1.ModelRegistryPhaseDELETED
	if deleteErr != nil && !isForceDelete {
		phase = v1.ModelRegistryPhaseFAILED
	}

	updateErr := c.updateStatus(obj, phase, deleteErr, currentStats(obj))
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

func currentStats(obj *v1.ModelRegistry) *v1.ModelRegistryStats {
	if obj.Status == nil {
		return nil
	}

	return obj.Status.Stats
}

func (c *ModelRegistryController) updateStatus(obj *v1.ModelRegistry, phase v1.ModelRegistryPhase,
	err error, stats *v1.ModelRegistryStats) error {
	newStatus := &v1.ModelRegistryStatus{
		LastTransitionTime: FormatStatusTime(),
		Phase:              phase,
		ErrorMessage:       FormatErrorForStatus(err),
		// PostgREST replaces a composite-type column as a whole, so any attribute
		// missing from the PATCH body is nulled rather than left alone. Stats is
		// therefore always passed in — carried forward from the observed object
		// when this reconcile did not recompute it — or every phase or error
		// transition wipes it. Same reason as ClusterController.updateStatus.
		Stats: stats,
	}

	return c.storage.UpdateModelRegistry(strconv.Itoa(obj.ID), &v1.ModelRegistry{Status: newStatus})
}
