package e2e

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	. "github.com/onsi/ginkgo/v2"
	"gopkg.in/yaml.v3"
)

const (
	// cleanupCallTimeout caps a delete request so a hung API cannot block
	// AfterSuite indefinitely after `go test -timeout 0` removes Go's runtime
	// backstop.
	cleanupCallTimeout = 30 * time.Second
	// cleanupWaitTimeout gives the controller enough time to finish one
	// dependency layer before cleanup attempts its parent resources.
	cleanupWaitTimeout = 5 * time.Minute
	// cleanupNodeTimeout bounds all dependency layers when AfterSuite runs
	// after a Ginkgo interruption.
	cleanupNodeTimeout = 30 * time.Minute
)

const (
	trackedCluster          = "cluster"
	trackedEndpoint         = "endpoint"
	trackedExternalEndpoint = "externalendpoint"
	trackedImageRegistry    = "imageregistry"
	trackedModelRegistry    = "modelregistry"
)

// trackedResource records a lifecycle resource created by the current E2E
// process. AfterSuite force-deletes entries whose normal cleanup did not
// complete after a timeout or interruption.
type trackedResource struct {
	Kind      string
	Name      string
	Workspace string
}

type manifestResource struct {
	Kind     string `yaml:"kind"`
	Metadata struct {
		Name      string `yaml:"name"`
		Workspace string `yaml:"workspace"`
	} `yaml:"metadata"`
}

var (
	trackedMu        sync.Mutex
	trackedResources = make(map[trackedResource]struct{})
)

// trackResource registers a current-process lifecycle resource. It silently
// ignores unsupported kinds and names that do not belong to the current RunID.
func trackResource(kind, name, workspace string) {
	resource, ok := newTrackedResource(kind, name, workspace)
	if !ok {
		return
	}

	trackedMu.Lock()
	defer trackedMu.Unlock()

	trackedResources[resource] = struct{}{}
}

// untrackResource removes an entry after the CLI has confirmed deletion.
func untrackResource(kind, name, workspace string) {
	canonicalKind, ok := canonicalTrackedKind(kind)
	if !ok {
		return
	}

	if workspace == "" {
		workspace = profileWorkspace()
	}

	trackedMu.Lock()
	defer trackedMu.Unlock()

	delete(trackedResources, trackedResource{
		Kind:      canonicalKind,
		Name:      name,
		Workspace: workspace,
	})
}

func trackedResourceCount() int {
	trackedMu.Lock()
	defer trackedMu.Unlock()

	return len(trackedResources)
}

// snapshotTrackedResources returns the current registry in dependency-safe
// delete order without mutating it.
func snapshotTrackedResources() []trackedResource {
	trackedMu.Lock()
	resources := make([]trackedResource, 0, len(trackedResources))

	for resource := range trackedResources {
		resources = append(resources, resource)
	}
	trackedMu.Unlock()

	sort.Slice(resources, func(i, j int) bool {
		leftPriority := cleanupPriority(resources[i].Kind)
		rightPriority := cleanupPriority(resources[j].Kind)

		if leftPriority != rightPriority {
			return leftPriority < rightPriority
		}

		if resources[i].Kind != resources[j].Kind {
			return resources[i].Kind < resources[j].Kind
		}

		if resources[i].Workspace != resources[j].Workspace {
			return resources[i].Workspace < resources[j].Workspace
		}

		return resources[i].Name < resources[j].Name
	})

	return resources
}

func clearTrackedResources() {
	trackedMu.Lock()
	defer trackedMu.Unlock()

	trackedResources = make(map[trackedResource]struct{})
}

// cleanupTrackedResources uses a background context for unit tests. The suite
// uses cleanupTrackedResourcesWithContext so its NodeTimeout can stop cleanup.
func cleanupTrackedResources() {
	cleanupTrackedResourcesWithContext(context.Background())
}

// cleanupTrackedResources force-deletes current-process resources in
// dependency-safe waves. Parent deletion validators count live children, so
// each dependency layer must fully disappear before its parents are deleted.
func cleanupTrackedResourcesWithContext(ctx context.Context) {
	leftover := snapshotTrackedResources()

	clearTrackedResources()

	if len(leftover) == 0 {
		return
	}

	fmt.Fprintf(GinkgoWriter,
		"AfterSuite: cleaning up %d tracked resource(s) left over from interrupted teardown\n",
		len(leftover))

	// External endpoints have no dependency on the other tracked kinds, so
	// their failure should not prevent endpoint/cluster cleanup.
	cleanupResourceGroup(ctx, leftover, trackedExternalEndpoint)

	if !cleanupResourceGroup(ctx, leftover, trackedEndpoint) {
		return
	}

	// Model registries only depend on endpoints and may be cleaned while
	// clusters are still finishing their own deletion.
	cleanupResourceGroup(ctx, leftover, trackedModelRegistry)

	if !cleanupResourceGroup(ctx, leftover, trackedCluster) {
		return
	}

	cleanupResourceGroup(ctx, leftover, trackedImageRegistry)
}

func cleanupResourceGroup(ctx context.Context, resources []trackedResource, kind string) bool {
	group := trackedResourcesOfKind(resources, kind)
	if len(group) == 0 {
		return true
	}

	deleteSucceeded := true

	for _, resource := range group {
		result := runCLIWithContext(ctx, cleanupCallTimeout,
			"delete", resource.Kind, resource.Name,
			"-w", resource.Workspace,
			"--force", "--ignore-not-found", "--wait=false",
		)
		if result.ExitCode != 0 {
			deleteSucceeded = false

			fmt.Fprintf(GinkgoWriter, "  delete %s/%s in %s failed (exit %d): %s\n",
				resource.Kind, resource.Name, resource.Workspace, result.ExitCode, result.Stderr)
		}
	}

	if !deleteSucceeded {
		return false
	}

	waitSucceeded := true

	for _, resource := range group {
		result := runCLIWithContext(ctx, cleanupWaitTimeout,
			"wait", resource.Kind, resource.Name,
			"-w", resource.Workspace,
			"--for", "delete",
			"--timeout", cleanupWaitTimeout.String(),
		)
		if result.ExitCode == 0 {
			fmt.Fprintf(GinkgoWriter, "  deleted %s/%s in %s\n",
				resource.Kind, resource.Name, resource.Workspace)
		} else {
			waitSucceeded = false

			fmt.Fprintf(GinkgoWriter, "  wait for %s/%s in %s failed (exit %d): %s\n",
				resource.Kind, resource.Name, resource.Workspace, result.ExitCode, result.Stderr)
		}
	}

	return waitSucceeded
}

func trackedResourcesOfKind(resources []trackedResource, kind string) []trackedResource {
	var group []trackedResource

	for _, resource := range resources {
		if resource.Kind == kind {
			group = append(group, resource)
		}
	}

	return group
}

// trackResourcesForApplyCommand records lifecycle resources from an apply
// manifest before the command runs. Pre-registration protects resources that
// are created successfully but whose following wait or test step is interrupted.
func trackResourcesForApplyCommand(args []string) {
	if len(args) == 0 || !strings.EqualFold(args[0], "apply") {
		return
	}

	manifestPath := commandFilePath(args)
	if manifestPath == "" {
		return
	}

	for _, resource := range trackedResourcesFromManifest(manifestPath, commandWorkspace(args)) {
		trackResource(resource.Kind, resource.Name, resource.Workspace)
	}
}

// reconcileTrackedResourcesAfterCommand updates the registry only after a
// successful CLI command. Callers pass it no failed command results.
func reconcileTrackedResourcesAfterCommand(args []string) {
	if len(args) == 0 {
		return
	}

	switch strings.ToLower(args[0]) {
	case "delete":
		if commandWaitsAsynchronously(args) {
			return
		}

		if manifestPath := commandFilePath(args); manifestPath != "" {
			for _, resource := range trackedResourcesFromManifest(manifestPath, commandWorkspace(args)) {
				untrackResource(resource.Kind, resource.Name, resource.Workspace)
			}

			return
		}

		if resource, ok := directCommandResource(args); ok {
			untrackResource(resource.Kind, resource.Name, resource.Workspace)
		}
	case "wait":
		if !waitsForDeletion(args) {
			return
		}

		if resource, ok := directCommandResource(args); ok {
			untrackResource(resource.Kind, resource.Name, resource.Workspace)
		}
	}
}

func trackedResourcesFromManifest(path, fallbackWorkspace string) []trackedResource {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil
	}

	decoder := yaml.NewDecoder(bytes.NewReader(data))
	resources := make(map[trackedResource]struct{})

	for {
		var manifest manifestResource

		err := decoder.Decode(&manifest)
		if err == io.EOF {
			break
		}

		if err != nil {
			return nil
		}

		workspace := manifest.Metadata.Workspace
		if workspace == "" {
			workspace = fallbackWorkspace
		}

		resource, ok := newTrackedResource(manifest.Kind, manifest.Metadata.Name, workspace)
		if ok {
			resources[resource] = struct{}{}
		}
	}

	result := make([]trackedResource, 0, len(resources))
	for resource := range resources {
		result = append(result, resource)
	}

	sort.Slice(result, func(i, j int) bool {
		if result[i].Kind != result[j].Kind {
			return result[i].Kind < result[j].Kind
		}

		if result[i].Workspace != result[j].Workspace {
			return result[i].Workspace < result[j].Workspace
		}

		return result[i].Name < result[j].Name
	})

	return result
}

func newTrackedResource(kind, name, workspace string) (trackedResource, bool) {
	canonicalKind, ok := canonicalTrackedKind(kind)
	if !ok || !isCurrentRunResource(name) {
		return trackedResource{}, false
	}

	if workspace == "" {
		workspace = profileWorkspace()
	}

	return trackedResource{
		Kind:      canonicalKind,
		Name:      name,
		Workspace: workspace,
	}, true
}

func canonicalTrackedKind(kind string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(kind)) {
	case "cluster", "clusters":
		return trackedCluster, true
	case "endpoint", "endpoints":
		return trackedEndpoint, true
	case "externalendpoint", "external-endpoint", "externalendpoints", "external-endpoints":
		return trackedExternalEndpoint, true
	case "imageregistry", "image-registry", "imageregistries", "image-registries":
		return trackedImageRegistry, true
	case "modelregistry", "model-registry", "modelregistries", "model-registries":
		return trackedModelRegistry, true
	default:
		return "", false
	}
}

func isCurrentRunResource(name string) bool {
	return strings.HasPrefix(name, "e2e-") && strings.HasSuffix(name, "-"+Cfg.RunID)
}

func cleanupPriority(kind string) int {
	switch kind {
	case trackedEndpoint, trackedExternalEndpoint:
		return 0
	case trackedCluster:
		return 1
	case trackedImageRegistry, trackedModelRegistry:
		return 2
	default:
		return 3
	}
}

func commandFilePath(args []string) string {
	for index, arg := range args {
		switch {
		case arg == "-f" || arg == "--file":
			if index+1 < len(args) {
				return args[index+1]
			}
		case strings.HasPrefix(arg, "--file="):
			return strings.TrimPrefix(arg, "--file=")
		}
	}

	return ""
}

func commandWorkspace(args []string) string {
	for index, arg := range args {
		switch {
		case arg == "-w" || arg == "--workspace":
			if index+1 < len(args) {
				return args[index+1]
			}
		case strings.HasPrefix(arg, "--workspace="):
			return strings.TrimPrefix(arg, "--workspace=")
		}
	}

	return profileWorkspace()
}

func commandWaitsAsynchronously(args []string) bool {
	for index, arg := range args {
		if arg == "--wait=false" {
			return true
		}

		if arg == "--wait" && index+1 < len(args) && args[index+1] == "false" {
			return true
		}
	}

	return false
}

func waitsForDeletion(args []string) bool {
	for index, arg := range args {
		if arg == "--for=delete" {
			return true
		}

		if arg == "--for" && index+1 < len(args) && args[index+1] == "delete" {
			return true
		}
	}

	return false
}

func directCommandResource(args []string) (trackedResource, bool) {
	if len(args) < 3 || strings.HasPrefix(args[1], "-") || strings.HasPrefix(args[2], "-") {
		return trackedResource{}, false
	}

	return newTrackedResource(args[1], args[2], commandWorkspace(args))
}

// runCLIWithTimeout is a context-bounded sibling of RunCLI used by unit tests
// and single cleanup calls without a Ginkgo node context.
func runCLIWithTimeout(timeout time.Duration, args ...string) CLIResult {
	return runCLIWithContext(context.Background(), timeout, args...)
}

// runCLIWithContext inherits the AfterSuite NodeTimeout and adds a per-command
// bound so a hung CLI process cannot block the remaining cleanup groups.
func runCLIWithContext(parent context.Context, timeout time.Duration, args ...string) CLIResult {
	ctx, cancel := context.WithTimeout(parent, timeout)
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
		if ctx.Err() != nil {
			return CLIResult{
				Stdout:   stdout.String(),
				Stderr:   strings.TrimSpace(stderr.String()) + " (cleanup command stopped: " + ctx.Err().Error() + ")",
				ExitCode: 124,
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
