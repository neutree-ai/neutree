package e2e

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
)

// cleanupCallTimeout caps each per-resource CLI delete in AfterSuite.
// Without this, a hung CP would leave AfterSuite blocked indefinitely:
// `go test -timeout 0` removed the Go runtime backstop, AfterSuite has no
// Ginkgo NodeTimeout, and grace-period only fires after upstream interrupt.
const cleanupCallTimeout = 30 * time.Second

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

// untrackResource removes ALL entries matching (kind, name, workspace) from
// the registry. Removing every duplicate is intentional and paired with the
// no-dedup-on-write policy in trackResource: writes are append-only for
// simplicity, removals are exhaustive so a balanced setup/teardown sequence
// (or a setup that registered twice and an idempotent single teardown)
// always lands the registry at zero entries for that triple.
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
		result := runCLIWithTimeout(cleanupCallTimeout,
			"delete", r.Kind, r.Name,
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

// runCLIWithTimeout is a context-bounded sibling of RunCLI used only by
// AfterSuite cleanup. RunCLI itself uses exec.Command without a context,
// so a hung CP would block AfterSuite forever in the post-`-timeout 0`
// world. AfterSuite is the only place this can deadlock the suite (in-spec
// hangs are bounded by --ginkgo.timeout + grace-period), so the surface is
// kept narrow here rather than retrofitting RunCLI globally.
func runCLIWithTimeout(timeout time.Duration, args ...string) CLIResult {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	injected := []string{
		"--server-url", Cfg.ServerURL,
		"--api-key", Cfg.APIKey,
		"--insecure",
	}
	fullArgs := make([]string, 0, len(injected)+len(args))
	fullArgs = append(fullArgs, injected...)
	fullArgs = append(fullArgs, args...)

	cmd := exec.CommandContext(ctx, cliBinary, fullArgs...)

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	exitCode := 0

	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return CLIResult{
				Stdout:   stdout.String(),
				Stderr:   strings.TrimSpace(stderr.String()) + " (timed out after " + timeout.String() + ")",
				ExitCode: 124, // conventional shell timeout exit code
			}
		}

		if exitErr, ok := err.(*exec.ExitError); ok {
			if status, ok := exitErr.Sys().(syscall.WaitStatus); ok {
				exitCode = status.ExitStatus()
			} else {
				exitCode = 1
			}
		} else {
			exitCode = 1
		}
	}

	return CLIResult{
		Stdout:   stdout.String(),
		Stderr:   stderr.String(),
		ExitCode: exitCode,
	}
}
