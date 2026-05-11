package e2e

import (
	"fmt"
	"sync"

	. "github.com/onsi/ginkgo/v2"
)

// trackedResource records a resource the current e2e session created so the
// suite-level AfterSuite safety net can force-delete it if a per-Describe
// AfterAll was truncated by Ginkgo's grace period.
type trackedResource struct {
	Kind      string // "cluster" | "modelregistry" | "imageregistry"
	Name      string
	Workspace string
}

var (
	trackedMu        sync.Mutex
	trackedResources []trackedResource
)

// trackResource registers a resource in the in-process cleanup registry.
// Setup helpers call this immediately after a successful create so the
// AfterSuite net can mop up if AfterAll never runs to completion.
func trackResource(kind, name, workspace string) {
	trackedMu.Lock()
	defer trackedMu.Unlock()

	trackedResources = append(trackedResources, trackedResource{
		Kind:      kind,
		Name:      name,
		Workspace: workspace,
	})
}

// untrackResource removes a resource from the registry. Teardown helpers call
// this after a successful delete so AfterSuite skips it.
func untrackResource(kind, name, workspace string) {
	trackedMu.Lock()
	defer trackedMu.Unlock()

	out := trackedResources[:0]
	for _, r := range trackedResources {
		if r.Kind == kind && r.Name == name && r.Workspace == workspace {
			continue
		}

		out = append(out, r)
	}

	trackedResources = out
}

// cleanupTrackedResources force-deletes every resource still in the registry,
// asynchronously (--wait=false) so AfterSuite is not blocked on K8s GC.
// Best-effort: failures are logged to GinkgoWriter without asserting, since
// AfterSuite failure would surface the suite as failed even when the actual
// specs passed.
//
// This catches two leak sources:
//   - Per-Describe AfterAll truncated by --ginkgo.grace-period (its own
//     EnsureDeleted blocks waiting for K8s namespace GC and gets killed
//     mid-flight).
//   - AfterAll never runs (panic in BeforeAll, suite-level interruption).
func cleanupTrackedResources() {
	trackedMu.Lock()
	leftover := append([]trackedResource(nil), trackedResources...)
	trackedResources = nil
	trackedMu.Unlock()

	if len(leftover) == 0 {
		return
	}

	fmt.Fprintf(GinkgoWriter,
		"AfterSuite: cleaning up %d tracked resource(s) left over from interrupted teardown\n",
		len(leftover))

	for _, r := range leftover {
		result := RunCLI("delete", r.Kind, r.Name,
			"-w", r.Workspace,
			"--force", "--ignore-not-found", "--wait=false",
		)
		if result.ExitCode == 0 {
			fmt.Fprintf(GinkgoWriter, "  deleted %s/%s in %s\n", r.Kind, r.Name, r.Workspace)
		} else {
			fmt.Fprintf(GinkgoWriter, "  delete %s/%s in %s failed (exit %d): %s\n",
				r.Kind, r.Name, r.Workspace, result.ExitCode, result.Stderr)
		}
	}
}
