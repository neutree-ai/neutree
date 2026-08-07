package model_registry

import (
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DefaultCheckedAtWriteInterval is how far the recorded check time is allowed to
// fall behind the real one.
//
// A reachability check runs on every reconcile, which is an order of magnitude
// more often than anyone reads the result. Persisting each one would turn an
// idle deployment into a steady stream of writes that say nothing new, so a
// check whose outcome matches what is already stored is only written back once
// this much time has passed. The cost is that the displayed check time trails
// the real one by up to one interval; what it buys is that "checked a minute
// ago" and "checked three days ago" remain distinguishable, which is the whole
// point of recording it.
const DefaultCheckedAtWriteInterval = time.Minute

// NextStatus builds the status to persist after a reachability check.
//
// Three timestamps-worth of policy live here, in one place, because both the
// reconcile loop and the on-demand retry endpoint have to agree on them:
//
//   - LastTransitionTime moves only when the phase does. It is the answer to
//     "since when has it been like this", so a re-check that confirms what was
//     already true must not reset it.
//   - LastCheckedAt moves every time, because that is what it is for.
//   - Stats are carried forward. PostgREST replaces a composite-type column as a
//     whole, so any attribute left out of the PATCH body is nulled rather than
//     left alone.
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

// NeedsStatusWrite reports whether the outcome of a check is worth persisting.
//
// It is deliberately not "did anything change": the check time has to keep
// moving even when nothing else does, or a registry nobody has looked at in a
// week is indistinguishable from one checked a second ago. So a write happens
// when the phase changed, when the error text changed — the text, not merely
// whether there was one, so that a failing registry stops showing a reason that
// no longer applies — when fresh statistics were collected, or when the recorded
// check time has fallen behind by more than checkInterval.
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
// worth rewriting. A missing or unparseable one always is: a status carried over
// from before this field existed has to start moving again somehow.
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
