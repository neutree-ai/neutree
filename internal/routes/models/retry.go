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

// retryConnectionResponse is the outcome of the check that was just run.
//
// A registry that turns out to be unreachable answers 200 with a phase of
// Failed. The request succeeded — the check ran and produced an answer — and the
// answer is the payload. Only a request that could not be carried out at all
// gets a failing status code.
type retryConnectionResponse struct {
	Phase              v1.ModelRegistryPhase `json:"phase"`
	ErrorMessage       string                `json:"error_message,omitempty"`
	LastCheckedAt      string                `json:"last_checked_at,omitempty"`
	LastTransitionTime string                `json:"last_transition_time,omitempty"`
}

// retryConnection checks a registry now and reports what it found.
//
// What this does *not* do is unblock a registry that would otherwise stay
// broken: a Failed registry is already disconnected and reconnected on every
// reconcile, so the connection would have been retried within the sync interval
// regardless. Presenting this as the only way back would be a lie about the
// system's behaviour.
//
// What it does do is worth having, and is the whole of its contract:
//
//   - it answers, so a user who has just fixed a mirror address or a token
//     learns the outcome from the click instead of watching a row for ten
//     seconds;
//   - it drops the cached query results for that registry, so the listing next
//     to the status is refetched rather than replayed from before the fix. This
//     is the part nothing else does — without it the status would go green while
//     the models next to it stayed stale, which reads as the retry not having
//     worked;
//   - it records the check, so the registry's last check time moves when a person
//     checks it and not only when the reconcile does.
//
// The check runs in this process, which is not the one that reconciles the
// registry. The two can genuinely disagree — different containers, so a mirror
// or an NFS export reachable from here need not be reachable from there — and
// when they do, the next reconcile overwrites what this wrote. That is the right
// resolution: the reconciling process is the one whose reachability decides
// whether models can actually be served.
//
// A disagreement is tolerable; a process that cannot perform the check at all is
// not, and for a private NFS registry the check mounts. This therefore depends
// on the API process being able to mount NFS, exactly as the reconciling one
// does — otherwise every healthy NFS registry would be reported Failed here, and
// that verdict written back. Both are deployed with the privileges and the
// mount.nfs binary that requires; the model listing handlers alongside this one
// already rely on it, since they connect the same way.
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

		// Dropped before the check rather than after it: if the check is slow, a
		// listing that arrives while it runs should already be missing the cache
		// rather than be served the results the user pressed this button to be rid
		// of.
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

		// Best effort: the caller asked for a check and is getting one. Failing the
		// request because the result could not be recorded would hide an answer we
		// already have, and the reconcile writes the same conclusion shortly after.
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

// checkModelRegistry runs the same sequence the reconcile runs against a
// registry it believes is connected: attach to it, ask whether it is actually
// usable, and let go. A configuration too broken to build a client from is a
// failed check, not an error to report separately — from the user's side it is
// the same "this registry does not work" with the same fix.
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
