package model_registry

import (
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// DefaultStatsStaleAfter is how long a registry's counters stay usable before
// they are worth recomputing.
//
// It has to be far above the reconcile interval. Measuring a registry means
// walking its whole model tree, which for a networked store is minutes of I/O;
// doing that on every reconcile would keep the control plane permanently busy
// re-measuring something that changes only when someone pushes a model.
const DefaultStatsStaleAfter = 10 * time.Minute

// StatsAggregator turns an observation of a registry into the stats block on its
// status. It keeps no state between calls: everything it needs comes in as an
// argument, so the same instance is safe to share and trivial to test.
type StatsAggregator struct {
	// StaleAfter overrides DefaultStatsStaleAfter. Zero means the default.
	StaleAfter time.Duration
	// Now overrides the clock. Nil means time.Now.
	Now func() time.Time
}

func (a StatsAggregator) now() time.Time {
	if a.Now == nil {
		return time.Now()
	}

	return a.Now()
}

func (a StatsAggregator) staleAfter() time.Duration {
	if a.StaleAfter <= 0 {
		return DefaultStatsStaleAfter
	}

	return a.StaleAfter
}

// NeedsRefresh reports whether previous is stale enough to be worth recomputing.
// Counters that have never been computed, or whose timestamp is missing or
// unparseable, always are.
func (a StatsAggregator) NeedsRefresh(previous *v1.ModelRegistryStats) bool {
	if previous == nil || previous.StatsUpdatedAt == "" {
		return true
	}

	updatedAt, err := time.Parse(time.RFC3339Nano, previous.StatsUpdatedAt)
	if err != nil {
		return true
	}

	return a.now().Sub(updatedAt) >= a.staleAfter()
}

// Aggregate builds the stats block from a fresh observation.
func (a StatsAggregator) Aggregate(observed *RegistryUsage) *v1.ModelRegistryStats {
	if observed == nil {
		return nil
	}

	return &v1.ModelRegistryStats{
		ModelCount:     observed.ModelCount,
		StorageBytes:   observed.StorageBytes,
		StatsUpdatedAt: a.now().Format(time.RFC3339Nano),
	}
}

// Refresh returns the stats to persist for a registry, measuring it only when
// the cached counters have gone stale. The second result says whether it
// measured; the caller needs it because a recomputation must be written back
// even when the counters came out identical — the stored timestamp is what
// stops the next reconcile from walking the tree again.
//
// It is best effort: a registry that cannot be measured — a public one, or a
// private one whose storage is unreachable — keeps whatever it last reported.
// Reporting zero instead would turn a transient mount failure into a registry
// that looks empty.
func (a StatsAggregator) Refresh(
	registry ModelRegistry,
	previous *v1.ModelRegistryStats,
	name string,
) (*v1.ModelRegistryStats, bool) {
	if !a.NeedsRefresh(previous) {
		return previous, false
	}

	observed, err := registry.CollectUsage()
	if err != nil {
		if !errors.Is(err, ErrNotSupported) {
			klog.Warningf("failed to collect usage of model registry %s: %v", name, err)
		}

		return previous, false
	}

	return a.Aggregate(observed), true
}
