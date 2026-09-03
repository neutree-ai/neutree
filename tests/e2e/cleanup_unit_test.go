package e2e

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// resetTrackedForTest clears the package-level registry both immediately and
// when the test ends, so the registry is empty entering the test AND when
// leaving it. The trailing reset matters: TestE2E runs in the same process
// after these unit tests, and a leaked entry would surface as a phantom
// resource in AfterSuite's cleanup pass.
func resetTrackedForTest(t *testing.T) {
	t.Helper()

	reset := func() {
		trackedMu.Lock()
		trackedResources = make(map[trackedResource]struct{})
		trackedMu.Unlock()
	}
	reset()
	t.Cleanup(reset)
}

func useRunIDForTest(t *testing.T, runID string) {
	t.Helper()

	original := Cfg.RunID
	Cfg.RunID = runID
	t.Cleanup(func() {
		Cfg.RunID = original
	})
}

func writeManifestForTest(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "resources.yaml")
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("write manifest: %v", err)
	}

	return path
}

func useCLIForTest(t *testing.T, content string) string {
	t.Helper()

	path := filepath.Join(t.TempDir(), "neutree-cli")
	if err := os.WriteFile(path, []byte(content), 0o700); err != nil {
		t.Fatalf("write fake CLI: %v", err)
	}

	original := cliBinary
	cliBinary = path
	t.Cleanup(func() {
		cliBinary = original
	})

	return path
}

func TestTrackResource_AppendsEntry(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-foo-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 entry, got %d", len(trackedResources))
	}
	found := false
	for got := range trackedResources {
		if got.Kind == "cluster" && got.Name == "e2e-foo-123456" && got.Workspace == "default" {
			found = true
		}
	}
	if !found {
		t.Fatalf("entry mismatch: %+v", trackedResources)
	}
}

func TestTrackResource_DeduplicatesExactResource(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-foo-123456", "default")
	trackResource("Cluster", "e2e-foo-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 deduplicated entry, got %d", len(trackedResources))
	}
}

func TestUntrackResource_RemovesByExactTriple(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-foo-123456", "default")
	trackResource("modelregistry", "e2e-mr-123456", "default")
	trackResource("imageregistry", "e2e-ir-123456", "default")

	untrackResource("modelregistry", "e2e-mr-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 2 {
		t.Fatalf("want 2 entries after untrack, got %d", len(trackedResources))
	}
	for r := range trackedResources {
		if r.Kind == "modelregistry" {
			t.Fatalf("modelregistry should be removed, still present: %+v", r)
		}
	}
}

func TestUntrackResource_NoMatch_NoOp(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-foo-123456", "default")

	// Different name -> no removal.
	untrackResource("cluster", "e2e-bar-123456", "default")
	// Different workspace -> no removal.
	untrackResource("cluster", "e2e-foo-123456", "other-ws")
	// Different kind -> no removal.
	untrackResource("modelregistry", "e2e-foo-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("untrack with non-matching key should be a no-op; want 1 entry, got %d", len(trackedResources))
	}
}

func TestUntrackResource_RemovesAllMatchingDuplicates(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-foo-123456", "default")
	trackResource("cluster", "e2e-other-123456", "default")

	untrackResource("cluster", "e2e-foo-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 entry after untrack, got %d", len(trackedResources))
	}
	for resource := range trackedResources {
		if resource.Name != "e2e-other-123456" {
			t.Fatalf("wrong remaining entry: %+v", resource)
		}
	}
}

func TestTrackResource_RejectsOtherRunAndUnsupportedKind(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	trackResource("cluster", "e2e-cluster-654321", "default")
	trackResource("role", "e2e-role-123456", "default")
	trackResource("cluster", "not-e2e-123456", "default")

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 0 {
		t.Fatalf("want no ineligible resources, got %+v", trackedResources)
	}
}

func TestTestEngineName_TracksCurrentRunEngine(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	name := testEngineName("e2e-engine-test")
	if name != "e2e-engine-test-123456" {
		t.Fatalf("engine name = %q", name)
	}

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if _, ok := trackedResources[trackedResource{
		Kind:      trackedEngine,
		Name:      name,
		Workspace: profileWorkspace(),
	}]; !ok {
		t.Fatalf("engine was not tracked: %+v", trackedResources)
	}
}

func TestTrackedResourcesFromManifest_SelectsCurrentLifecycleResources(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: Endpoint
metadata:
  name: e2e-endpoint-123456
  workspace: test-workspace
---
apiVersion: v1
kind: ExternalEndpoint
metadata:
  name: e2e-external-123456
---
apiVersion: v1
kind: Cluster
metadata:
  name: e2e-cluster-654321
  workspace: test-workspace
---
apiVersion: v1
kind: Engine
metadata:
  name: e2e-engine-123456
  workspace: test-workspace
---
apiVersion: v1
kind: ImageRegistry
metadata:
  name: not-e2e-123456
  workspace: test-workspace
`)

	got := trackedResourcesFromManifest(path, profileWorkspace())
	if len(got) != 3 {
		t.Fatalf("want 3 current lifecycle resources, got %+v", got)
	}
	if got[0] != (trackedResource{Kind: "endpoint", Name: "e2e-endpoint-123456", Workspace: "test-workspace"}) {
		t.Fatalf("unexpected first resource: %+v", got[0])
	}
	if got[1] != (trackedResource{Kind: "externalendpoint", Name: "e2e-external-123456", Workspace: profileWorkspace()}) {
		t.Fatalf("unexpected second resource: %+v", got[1])
	}
	if got[2] != (trackedResource{Kind: trackedEngine, Name: "e2e-engine-123456", Workspace: "test-workspace"}) {
		t.Fatalf("unexpected third resource: %+v", got[2])
	}
}

func TestTrackResourcesForApplyCommand_DeduplicatesMultiDocumentManifest(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: ModelRegistry
metadata:
  name: e2e-registry-123456
  workspace: default
---
apiVersion: v1
kind: ModelRegistry
metadata:
  name: e2e-registry-123456
  workspace: default
`)

	trackResourcesForApplyCommand([]string{"apply", "-f", path})

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if len(trackedResources) != 1 {
		t.Fatalf("want 1 deduplicated tracked resource, got %+v", trackedResources)
	}
}

func TestTrackedResourcesFromManifest_UsesCommandWorkspaceFallback(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: Endpoint
metadata:
  name: e2e-endpoint-123456
`)

	got := trackedResourcesFromManifest(path, "other-workspace")
	if len(got) != 1 {
		t.Fatalf("want 1 tracked resource, got %+v", got)
	}
	if got[0] != (trackedResource{
		Kind:      "endpoint",
		Name:      "e2e-endpoint-123456",
		Workspace: "other-workspace",
	}) {
		t.Fatalf("unexpected resource: %+v", got[0])
	}
}

func TestTrackedResourcesFromManifest_RejectsMalformedManifest(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, "kind: Endpoint\nmetadata: [")

	if got := trackedResourcesFromManifest(path, profileWorkspace()); len(got) != 0 {
		t.Fatalf("malformed manifest should not register resources, got %+v", got)
	}
}

func TestRunCLIWithStdin_TracksApplyAndWaitsForConfirmedDelete(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	useCLIForTest(t, "#!/bin/sh\nexit 0\n")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: Endpoint
metadata:
  name: e2e-endpoint-123456
  workspace: default
`)

	result := RunCLI("apply", "--file="+path)
	if result.ExitCode != 0 {
		t.Fatalf("apply result = %+v", result)
	}
	if trackedResourceCount() != 1 {
		t.Fatalf("apply should pre-register current resource")
	}

	result = RunCLI("delete", "endpoint", "e2e-endpoint-123456", "-w", "default", "--wait=false")
	if result.ExitCode != 0 || trackedResourceCount() != 1 {
		t.Fatalf("async delete should retain resource, result=%+v count=%d", result, trackedResourceCount())
	}

	result = RunCLI("wait", "endpoint", "e2e-endpoint-123456", "-w", "default", "--for=delete")
	if result.ExitCode != 0 || trackedResourceCount() != 0 {
		t.Fatalf("successful delete wait should untrack resource, result=%+v count=%d", result, trackedResourceCount())
	}
}

func TestRunCLIWithStdin_ForwardsInputArgsAndExitCode(t *testing.T) {
	argsPath := filepath.Join(t.TempDir(), "args.txt")
	stdinPath := filepath.Join(t.TempDir(), "stdin.txt")
	useCLIForTest(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" > %q\ncat > %q\nexit 7\n", argsPath, stdinPath))

	result := RunCLIWithStdin("input", "get", "cluster")
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d, want 7; result=%+v", result.ExitCode, result)
	}

	args, err := os.ReadFile(argsPath)
	if err != nil {
		t.Fatalf("read args: %v", err)
	}
	for _, want := range []string{"--server-url", "--api-key", "--insecure", "get cluster"} {
		if !strings.Contains(string(args), want) {
			t.Fatalf("arguments %q do not contain %q", args, want)
		}
	}

	stdin, err := os.ReadFile(stdinPath)
	if err != nil {
		t.Fatalf("read stdin: %v", err)
	}
	if string(stdin) != "input" {
		t.Fatalf("stdin = %q, want %q", stdin, "input")
	}
}

func TestReconcileTrackedResourcesAfterCommand(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")

	resource := trackedResource{Kind: "endpoint", Name: "e2e-endpoint-123456", Workspace: "default"}
	trackResource(resource.Kind, resource.Name, resource.Workspace)

	reconcileTrackedResourcesAfterCommand([]string{"delete", "endpoint", resource.Name, "-w", resource.Workspace})
	if trackedResourceCount() != 0 {
		t.Fatalf("synchronous delete should untrack resource")
	}

	trackResource(resource.Kind, resource.Name, resource.Workspace)
	reconcileTrackedResourcesAfterCommand([]string{"delete", "endpoint", resource.Name, "-w", resource.Workspace, "--wait=false"})
	if trackedResourceCount() != 1 {
		t.Fatalf("asynchronous delete should retain resource until wait succeeds")
	}

	reconcileTrackedResourcesAfterCommand([]string{"wait", "endpoint", resource.Name, "-w", resource.Workspace, "--for", "delete"})
	if trackedResourceCount() != 0 {
		t.Fatalf("successful delete wait should untrack resource")
	}
}

func TestReconcileTrackedResourcesAfterManifestDelete(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: ModelRegistry
metadata:
  name: e2e-registry-123456
  workspace: default
`)

	trackResourcesForApplyCommand([]string{"apply", "-f", path})
	if trackedResourceCount() != 1 {
		t.Fatalf("apply should register manifest resource")
	}

	reconcileTrackedResourcesAfterCommand([]string{"delete", "-f", path, "--force"})
	if trackedResourceCount() != 0 {
		t.Fatalf("successful manifest delete should untrack resource")
	}
}

func TestTrackResourcesForApplyCommand_UsesCommandWorkspaceFallback(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	path := writeManifestForTest(t, `
apiVersion: v1
kind: Endpoint
metadata:
  name: e2e-endpoint-123456
`)

	trackResourcesForApplyCommand([]string{"apply", "-f", path, "-w", "other-workspace"})
	if trackedResourceCount() != 1 {
		t.Fatalf("apply should register resource with command workspace fallback")
	}

	trackedMu.Lock()
	defer trackedMu.Unlock()
	if _, ok := trackedResources[trackedResource{
		Kind:      "endpoint",
		Name:      "e2e-endpoint-123456",
		Workspace: "other-workspace",
	}]; !ok {
		t.Fatalf("tracked resources = %+v", trackedResources)
	}
}

func TestCleanupTrackedResources_DeletesInDependencyOrder(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	logPath := filepath.Join(t.TempDir(), "cli.log")
	useCLIForTest(t, fmt.Sprintf("#!/bin/sh\nprintf '%%s\\n' \"$*\" >> %q\nexit 0\n", logPath))

	trackResource("modelregistry", "e2e-registry-123456", "default")
	trackResource("engine", "e2e-engine-123456", "default")
	trackResource("cluster", "e2e-cluster-123456", "default")
	trackResource("endpoint", "e2e-endpoint-123456", "default")

	cleanupTrackedResourcesWithContext(context.Background())
	if trackedResourceCount() != 0 {
		t.Fatalf("cleanup should clear current-process registry")
	}

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake CLI log: %v", err)
	}
	output := string(data)
	endpointIndex := strings.Index(output, "delete endpoint e2e-endpoint-123456")
	endpointWaitIndex := strings.Index(output, "wait endpoint e2e-endpoint-123456")
	registryIndex := strings.Index(output, "delete modelregistry e2e-registry-123456")
	registryWaitIndex := strings.Index(output, "wait modelregistry e2e-registry-123456")
	engineIndex := strings.Index(output, "delete engine e2e-engine-123456")
	engineWaitIndex := strings.Index(output, "wait engine e2e-engine-123456")
	clusterIndex := strings.Index(output, "delete cluster e2e-cluster-123456")
	clusterWaitIndex := strings.Index(output, "wait cluster e2e-cluster-123456")
	if endpointIndex < 0 || endpointWaitIndex < 0 || registryIndex < 0 || registryWaitIndex < 0 || engineIndex < 0 || engineWaitIndex < 0 || clusterIndex < 0 || clusterWaitIndex < 0 {
		t.Fatalf("missing cleanup command(s): %q", output)
	}
	if endpointIndex > endpointWaitIndex || endpointWaitIndex > registryIndex || registryIndex > registryWaitIndex || registryWaitIndex > engineIndex || engineIndex > engineWaitIndex || engineWaitIndex > clusterIndex || clusterIndex > clusterWaitIndex {
		t.Fatalf("cleanup order is not dependency-safe: %q", output)
	}
}

func TestCleanupTrackedResources_StopsDependentCleanupAfterEndpointDeleteFailure(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	logPath := filepath.Join(t.TempDir(), "cli.log")
	useCLIForTest(t, fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"delete endpoint e2e-endpoint-123456"*) exit 1 ;;
esac
exit 0
`, logPath))

	trackResource("endpoint", "e2e-endpoint-123456", "default")
	trackResource("engine", "e2e-engine-123456", "default")
	trackResource("cluster", "e2e-cluster-123456", "default")
	trackResource("imageregistry", "e2e-image-registry-123456", "default")

	cleanupTrackedResourcesWithContext(context.Background())

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake CLI log: %v", err)
	}
	output := string(data)
	if strings.Contains(output, "delete cluster e2e-cluster-123456") {
		t.Fatalf("cluster cleanup must wait for endpoint cleanup success: %q", output)
	}
	if strings.Contains(output, "delete engine e2e-engine-123456") {
		t.Fatalf("engine cleanup must wait for endpoint cleanup success: %q", output)
	}
	if strings.Contains(output, "delete imageregistry e2e-image-registry-123456") {
		t.Fatalf("registry cleanup must wait for cluster cleanup success: %q", output)
	}
}

func TestCleanupTrackedResources_WaitsSuccessfulDeletesAfterPartialFailure(t *testing.T) {
	resetTrackedForTest(t)
	useRunIDForTest(t, "123456")
	logPath := filepath.Join(t.TempDir(), "cli.log")
	useCLIForTest(t, fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"delete endpoint e2e-failed-123456"*) exit 1 ;;
esac
exit 0
`, logPath))

	trackResource("endpoint", "e2e-successful-123456", "default")
	trackResource("endpoint", "e2e-failed-123456", "default")
	trackResource("cluster", "e2e-cluster-123456", "default")

	cleanupTrackedResourcesWithContext(context.Background())

	data, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatalf("read fake CLI log: %v", err)
	}
	output := string(data)
	if !strings.Contains(output, "wait endpoint e2e-successful-123456") {
		t.Fatalf("successful delete was not waited: %q", output)
	}
	if strings.Contains(output, "wait endpoint e2e-failed-123456") {
		t.Fatalf("failed delete must not be waited: %q", output)
	}
	if strings.Contains(output, "delete cluster e2e-cluster-123456") {
		t.Fatalf("cluster cleanup must stop after partial endpoint delete failure: %q", output)
	}
}

func TestCleanupTrackedResources_ContinuesAfterIndependentDeleteFailure(t *testing.T) {
	for _, kind := range []string{trackedModelRegistry, trackedEngine} {
		t.Run(kind, func(t *testing.T) {
			resetTrackedForTest(t)
			useRunIDForTest(t, "123456")
			logPath := filepath.Join(t.TempDir(), "cli.log")
			useCLIForTest(t, fmt.Sprintf(`#!/bin/sh
printf '%%s\n' "$*" >> %q
case "$*" in
  *"delete %s e2e-%s-123456"*) exit 1 ;;
esac
exit 0
`, logPath, kind, kind))

			trackResource(kind, "e2e-"+kind+"-123456", "default")
			trackResource(trackedCluster, "e2e-cluster-123456", "default")
			trackResource(trackedImageRegistry, "e2e-image-registry-123456", "default")

			cleanupTrackedResourcesWithContext(context.Background())

			data, err := os.ReadFile(logPath)
			if err != nil {
				t.Fatalf("read fake CLI log: %v", err)
			}
			output := string(data)
			if !strings.Contains(output, "delete cluster e2e-cluster-123456") {
				t.Fatalf("cluster cleanup should continue after %s failure: %q", kind, output)
			}
			if !strings.Contains(output, "delete imageregistry e2e-image-registry-123456") {
				t.Fatalf("image registry cleanup should continue after %s failure: %q", kind, output)
			}
		})
	}
}

func TestRunCleanupCommands_RunsSameWaveInParallel(t *testing.T) {
	resources := []trackedResource{
		{Kind: trackedEndpoint, Name: "e2e-first-123456", Workspace: "default"},
		{Kind: trackedEndpoint, Name: "e2e-second-123456", Workspace: "default"},
	}
	started := make(chan struct{}, len(resources))
	release := make(chan struct{})
	defer func() {
		select {
		case <-release:
		default:
			close(release)
		}
	}()

	done := make(chan []cleanupCommandResult, 1)
	go func() {
		done <- runCleanupCommands(resources, func(trackedResource) CLIResult {
			started <- struct{}{}
			<-release
			return CLIResult{}
		})
	}()

	for range resources {
		select {
		case <-started:
		case <-time.After(time.Second):
			t.Fatal("cleanup commands did not start in parallel")
		}
	}
	close(release)

	if results := <-done; len(results) != len(resources) {
		t.Fatalf("result count = %d, want %d", len(results), len(resources))
	}
}

func TestRunCLIWithContext_ReturnsTimeoutExitCode(t *testing.T) {
	useCLIForTest(t, "#!/bin/sh\nexec sleep 1\n")

	result := runCLIWithContext(context.Background(), 10*time.Millisecond, "get", "cluster")
	if result.ExitCode != 124 {
		t.Fatalf("timeout exit code = %d, want 124; result=%+v", result.ExitCode, result)
	}
}
