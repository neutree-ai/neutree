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
	// checkedAtWriteInterval is how far the recorded check time may trail the real
	// one before an otherwise unchanged status is written back.
	checkedAtWriteInterval time.Duration
	// now overrides the clock in tests.
	now func() time.Time

	syncHandler func(modelRegistry *v1.ModelRegistry) error
}

type ModelRegistryControllerOption struct {
	Storage storage.Storage
	// StatsStaleAfter overrides how long collected registry counters stay usable
	// before the tree is walked again. Zero uses the package default.
	StatsStaleAfter time.Duration
	// CheckedAtWriteInterval overrides how often an unchanged reachability result
	// is persisted purely to advance its timestamp. Zero uses the package default.
	CheckedAtWriteInterval time.Duration
}

func NewModelRegistryController(option *ModelRegistryControllerOption) (*ModelRegistryController, error) {
	c := &ModelRegistryController{
		storage:                option.Storage,
		stats:                  model_registry.StatsAggregator{StaleAfter: option.StatsStaleAfter},
		checkedAtWriteInterval: option.CheckedAtWriteInterval,
	}

	c.syncHandler = c.sync

	return c, nil
}

func (c *ModelRegistryController) clock() time.Time {
	if c.now == nil {
		return time.Now()
	}

	return c.now()
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

	// Defer block to handle status updates for non-deletion paths. Every path
	// through this function below this point has checked whether the registry
	// answers, so reaching here is what "checked" means.
	defer func() {
		// Determine phase based on error
		phase := v1.ModelRegistryPhaseCONNECTED
		if err != nil {
			phase = v1.ModelRegistryPhaseFAILED
		}

		errMessage := FormatErrorForStatus(err)
		now := c.clock()

		// Not every check is worth a write. What is written back, and when, is
		// decided in one place shared with the on-demand retry path — including the
		// throttle that keeps an unchanged result from being persisted ten times a
		// minute purely to move its timestamp.
		if !model_registry.NeedsStatusWrite(obj.Status, phase, errMessage, statsRefreshed,
			now, c.checkedAtWriteInterval) {
			return
		}

		newStatus := model_registry.NextStatus(obj.Status, phase, errMessage, now)
		newStatus.Stats = stats

		updateErr := c.updateStatus(obj, newStatus)
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
		// Measured only here, on a registry known to be reachable right now, and
		// only when the previous counters have gone stale. This is also why an
		// unreachable registry keeps its last known counters: the code that would
		// overwrite them never runs.
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

	// A deletion is not a reachability check, so the recorded check time is
	// carried over rather than moved: what happened here says nothing about
	// whether the registry is answering.
	newStatus := model_registry.NextStatus(obj.Status, phase, FormatErrorForStatus(deleteErr), c.clock())
	newStatus.LastCheckedAt = currentCheckedAt(obj)

	updateErr := c.updateStatus(obj, newStatus)
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

func currentCheckedAt(obj *v1.ModelRegistry) string {
	if obj.Status == nil {
		return ""
	}

	return obj.Status.LastCheckedAt
}

// updateStatus persists a status built by model_registry.NextStatus.
//
// PostgREST replaces a composite-type column as a whole, so any attribute
// missing from the PATCH body is nulled rather than left alone. Every attribute
// therefore has to be present in what is passed here — carried forward from the
// observed object when this reconcile did not produce a new value — or a phase
// change wipes the statistics and the check time along with it. Same reason as
// ClusterController.updateStatus.
func (c *ModelRegistryController) updateStatus(obj *v1.ModelRegistry, newStatus *v1.ModelRegistryStatus) error {
	return c.storage.UpdateModelRegistry(strconv.Itoa(obj.ID), &v1.ModelRegistry{Status: newStatus})
}
