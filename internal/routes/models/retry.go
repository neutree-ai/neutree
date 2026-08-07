package models

import (
	"net/http"
	"strconv"
	"time"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
)

// retryConnectionResponse is the outcome of the check that was just run. An
// unreachable registry answers 200 with phase Failed: the check ran and produced
// an answer, and the answer is the payload.
type retryConnectionResponse struct {
	Phase              v1.ModelRegistryPhase `json:"phase"`
	ErrorMessage       string                `json:"error_message,omitempty"`
	LastCheckedAt      string                `json:"last_checked_at,omitempty"`
	LastTransitionTime string                `json:"last_transition_time,omitempty"`
}

// retryConnection checks a registry now and reports what it found.
//
// It does not unblock anything: a Failed registry is disconnected and
// reconnected on every reconcile anyway. What it adds is an immediate answer, a
// recorded check time, and — the part nothing else does — invalidation of the
// cached query results, without which a refreshed status would sit next to
// listings fetched before the fix.
//
// The check runs in this process, which is not the one that reconciles the
// registry, so the two can disagree and the next reconcile wins. That also means
// this process must be able to do what the check requires — for an NFS registry,
// mount — or every healthy NFS registry is reported Failed here and that verdict
// written back.
func retryConnection(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		registry, err := findModelRegistry(c, deps)
		if err != nil {
			klog.Errorf("Failed to get model registry: %v", err)
			c.JSON(http.StatusInternalServerError, gin.H{
				"message": err.Error(),
			})

			return
		}

		// Dropped before the check, so a listing that arrives while a slow check runs
		// already misses the cache.
		deps.QueryCache.Invalidate(registry)

		checkErr := checkModelRegistry(registry)

		phase := v1.ModelRegistryPhaseCONNECTED
		errMessage := ""

		if checkErr != nil {
			phase = v1.ModelRegistryPhaseFAILED
			errMessage = checkErr.Error()

			klog.Warningf("Model registry %s/%s failed an on-demand connection check: %v",
				registry.Metadata.Workspace, registry.Metadata.Name, checkErr)
		}

		status := model_registry.NextStatus(registry.Status, phase, errMessage, time.Now())

		// Best effort: the caller is owed the answer even if it cannot be recorded, and
		// the reconcile writes the same conclusion shortly after.
		if err := deps.Storage.UpdateModelRegistry(strconv.Itoa(registry.ID),
			&v1.ModelRegistry{Status: status}); err != nil {
			klog.Errorf("Failed to record connection check of model registry %s/%s: %v",
				registry.Metadata.Workspace, registry.Metadata.Name, err)
		}

		c.JSON(http.StatusOK, retryConnectionResponse{
			Phase:              status.Phase,
			ErrorMessage:       status.ErrorMessage,
			LastCheckedAt:      status.LastCheckedAt,
			LastTransitionTime: status.LastTransitionTime,
		})
	}
}

// checkModelRegistry attaches to the registry, asks whether it is usable, and
// lets go. A configuration too broken to build a client from is a failed check,
// not a separate error: the user sees the same "this registry does not work".
func checkModelRegistry(registry *v1.ModelRegistry) error {
	client, err := model_registry.NewModelRegistry(registry)
	if err != nil {
		return err
	}

	if err = client.Connect(); err != nil {
		return err
	}

	defer client.Disconnect() //nolint:errcheck

	return client.HealthyCheck()
}
