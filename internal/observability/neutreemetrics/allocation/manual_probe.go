package allocation

import (
	"os"
	"sync/atomic"
	"time"

	"k8s.io/klog/v2"
)

// Manual A/B harness for NEU-808. Test branch only: it exists so the change can
// be measured on one node, in one binary, against one actor population, instead
// of comparing two deployments whose actors differ.
//
// The question it answers is cost: how many process reads one collection does,
// and how long it takes.
//
//	NEUTREE_STATIC_TREE=legacy|snapshot
//	  legacy   - read the process tree per query, the way it was before
//	             CachedProcessDescendantReader (the real old reader, unchanged)
//	  snapshot - default, one enumeration per collection
//
//	NEUTREE_STATIC_KEEP_STALE=true|false
//	  true  - keep DEAD actors and unmatched replicas, the pre-filter behaviour
//	  false - default, drop both
//	  Secondary, and it moves nothing below: dropping stale entries changes what
//	  the evidence carries, not how much is read. It is here only because the
//	  stale-evidence defect shares this path, and its visible half - the node
//	  metrics going missing - is the Ascend adapter's behaviour, so it does not
//	  reproduce on nvidia.
//
// The counters sit on the two choke points every process read goes through, so
// the same number is comparable across the modes.
const (
	treeModeEnv  = "NEUTREE_STATIC_TREE"
	keepStaleEnv = "NEUTREE_STATIC_KEEP_STALE"

	treeModeLegacy   = "legacy"
	treeModeSnapshot = "snapshot"
)

var probeCounters struct {
	readDir    atomic.Int64
	statusRead atomic.Int64
}

func resetProbe() {
	probeCounters.readDir.Store(0)
	probeCounters.statusRead.Store(0)
}

func countReadDir()    { probeCounters.readDir.Add(1) }
func countStatusRead() { probeCounters.statusRead.Add(1) }

func legacyTree() bool { return os.Getenv(treeModeEnv) == treeModeLegacy }

func keepStale() bool { return os.Getenv(keepStaleEnv) == "true" }

func treeMode() string {
	if legacyTree() {
		return treeModeLegacy
	}

	return treeModeSnapshot
}

func reportCollection(actors, live, replicas, kept int, started time.Time) {
	klog.Infof(
		"NEU-808 probe: tree=%s keep_stale=%t actors=%d live=%d replicas=%d kept=%d readdir=%d status_reads=%d elapsed=%s",
		treeMode(), keepStale(), actors, live, replicas, kept,
		probeCounters.readDir.Load(), probeCounters.statusRead.Load(),
		time.Since(started).Round(time.Millisecond),
	)
}
