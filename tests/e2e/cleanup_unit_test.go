package e2e

import (
	"testing"
)

// resetTrackedForTest clears the package-level registry so each subtest
// starts from a known state. Direct test on package-private state is fine
// since this file is in the e2e package.
func resetTrackedForTest(t *testing.T) {
	t.Helper()
	trackedMu.Lock()
	trackedResources = nil
	trackedMu.Unlock()
}

func TestTrackResource_AppendsEntry(t *testing.T) {
	resetTrackedForTest(t)

	trackResource("cluster", "e2e-foo", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 entry, got %d", len(trackedResources))
	}
	got := trackedResources[0]
	if got.Kind != "cluster" || got.Name != "e2e-foo" || got.Workspace != "default" {
		t.Fatalf("entry mismatch: %+v", got)
	}
}

func TestTrackResource_AllowsDuplicates(t *testing.T) {
	// Duplicate registrations should each appear separately. cleanupTracked-
	// Resources uses --ignore-not-found, so duplicate deletes are harmless,
	// and we prefer simple-and-correct over dedup-on-write.
	resetTrackedForTest(t)

	trackResource("cluster", "e2e-foo", "default")
	trackResource("cluster", "e2e-foo", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 2 {
		t.Fatalf("want 2 entries (dedup not expected), got %d", len(trackedResources))
	}
}

func TestUntrackResource_RemovesByExactTriple(t *testing.T) {
	resetTrackedForTest(t)

	trackResource("cluster", "e2e-foo", "default")
	trackResource("modelregistry", "e2e-mr", "default")
	trackResource("imageregistry", "e2e-ir", "default")

	untrackResource("modelregistry", "e2e-mr", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 2 {
		t.Fatalf("want 2 entries after untrack, got %d", len(trackedResources))
	}
	for _, r := range trackedResources {
		if r.Kind == "modelregistry" {
			t.Fatalf("modelregistry should be removed, still present: %+v", r)
		}
	}
}

func TestUntrackResource_NoMatch_NoOp(t *testing.T) {
	resetTrackedForTest(t)

	trackResource("cluster", "e2e-foo", "default")

	// Different name -> no removal.
	untrackResource("cluster", "e2e-bar", "default")
	// Different workspace -> no removal.
	untrackResource("cluster", "e2e-foo", "other-ws")
	// Different kind -> no removal.
	untrackResource("modelregistry", "e2e-foo", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("untrack with non-matching key should be a no-op; want 1 entry, got %d", len(trackedResources))
	}
}

func TestUntrackResource_RemovesAllMatchingDuplicates(t *testing.T) {
	resetTrackedForTest(t)

	trackResource("cluster", "e2e-foo", "default")
	trackResource("cluster", "e2e-foo", "default")
	trackResource("cluster", "e2e-other", "default")

	untrackResource("cluster", "e2e-foo", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 entry after removing both duplicates, got %d", len(trackedResources))
	}
	if trackedResources[0].Name != "e2e-other" {
		t.Fatalf("wrong remaining entry: %+v", trackedResources[0])
	}
}
