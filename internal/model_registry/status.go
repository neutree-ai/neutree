package model_registry

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DefaultCheckedAtWriteInterval is how far LastCheckedAt may trail the real
// check before an otherwise unchanged status is written back. A reachability
// check runs on every reconcile, so without a throttle an idle deployment writes
// its status continuously to say nothing new; with one, the displayed check time
// is stale by up to an interval.
const DefaultCheckedAtWriteInterval = time.Minute

// NextStatus builds the status to persist after a reachability check. The
// reconcile loop and the retry endpoint both go through it so that they cannot
// disagree:
//
//   - LastTransitionTime moves only when the phase does, so a re-check that
//     confirms what was already true does not reset it.
//   - LastCheckedAt moves every time.
//   - Stats are carried forward. PostgREST replaces a composite-type column as a
//     whole, so any attribute missing from the PATCH body is nulled. Every
//     attribute added to ModelRegistryStatus has to be carried here too.
func NextStatus(previous *v1.ModelRegistryStatus, phase v1.ModelRegistryPhase,
	errMessage string, now time.Time) *v1.ModelRegistryStatus {
	next := &v1.ModelRegistryStatus{
		Phase:              phase,
		ErrorMessage:       errMessage,
		LastCheckedAt:      now.Format(time.RFC3339Nano),
		LastTransitionTime: now.Format(time.RFC3339Nano),
	}

	if previous == nil {
		return next
	}

	next.Stats = previous.Stats

	if previous.Phase == phase && previous.LastTransitionTime != "" {
		next.LastTransitionTime = previous.LastTransitionTime
	}

	return next
}

// NeedsStatusWrite reports whether the outcome of a check is worth persisting:
// the phase changed, the error text changed, statistics were collected, or the
// recorded check time has fallen behind by more than checkInterval.
//
// The error comparison is on the text, not on whether there is one, so that a
// registry that keeps failing for a new reason stops displaying the old one.
func NeedsStatusWrite(previous *v1.ModelRegistryStatus, phase v1.ModelRegistryPhase,
	errMessage string, statsRefreshed bool, now time.Time, checkInterval time.Duration) bool {
	if statsRefreshed || previous == nil {
		return true
	}

	if previous.Phase != phase || previous.ErrorMessage != errMessage {
		return true
	}

	return checkedAtIsStale(previous.LastCheckedAt, now, checkInterval)
}

// checkedAtIsStale reports whether a recorded check time is old enough to be
// worth rewriting. A missing or unparseable one always is, so a status written
// before this field existed starts moving again.
func checkedAtIsStale(lastCheckedAt string, now time.Time, checkInterval time.Duration) bool {
	if lastCheckedAt == "" {
		return true
	}

	checkedAt, err := time.Parse(time.RFC3339Nano, lastCheckedAt)
	if err != nil {
		return true
	}

	if checkInterval <= 0 {
		checkInterval = DefaultCheckedAtWriteInterval
	}

	return now.Sub(checkedAt) >= checkInterval
}
