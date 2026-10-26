//go:build unix

package bench

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type localCommandRunner struct{}

func (localCommandRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	process := exec.CommandContext(ctx, "sh", "-c", command)
	process.Stdout = stdout
	process.Stderr = stderr

	return process.Run()
}

func waitForPID(t *testing.T, path string) int {
	t.Helper()

	var pid int
	require.Eventually(t, func() bool {
		data, err := os.ReadFile(path)
		if err != nil {
			return false
		}

		value, err := strconv.Atoi(strings.TrimSpace(string(data)))
		if err != nil || value <= 0 {
			return false
		}

		pid = value
		return true
	}, 5*time.Second, 20*time.Millisecond)

	return pid
}

func TestControlledBenchmarkCancellationStopsOnlyItsProcessGroup(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}

	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

	unrelated := exec.Command("sleep", "30")
	require.NoError(t, unrelated.Start())
	t.Cleanup(func() {
		_ = unrelated.Process.Kill()
		_ = unrelated.Wait()
	})

	ready := filepath.Join(root, "benchmark.pid")
	childReady := filepath.Join(root, "child-ready")
	survived := filepath.Join(root, "benchmark-survived")
	script := `import os, subprocess, sys, time
child = '''import signal, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with open(sys.argv[1], "w") as f:
    f.write("ready")
time.sleep(4)
with open(sys.argv[2], "w") as f:
    f.write("still running")
'''
subprocess.Popen([sys.executable, "-c", child, sys.argv[2], sys.argv[3]])
while not os.path.exists(sys.argv[2]):
    time.sleep(0.01)
with open(sys.argv[1], "w") as f:
    f.write(str(os.getpid()))
time.sleep(15)
`
	command := "exec python3 -c " + shellQuote(script) + " " + shellQuote(ready) + " " + shellQuote(childReady) + " " + shellQuote(survived)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() {
		done <- runControlledBenchmark(ctx, runner, remoteDir, command, io.Discard, io.Discard)
	}()
	t.Cleanup(func() {
		cancel()
		select {
		case <-done:
		case <-time.After(15 * time.Second):
			t.Error("benchmark did not stop")
		}
	})

	pid := waitForPID(t, ready)

	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		done <- err
	case <-time.After(10 * time.Second):
		t.Fatal("canceled benchmark did not stop")
	}

	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
	require.NoError(t, unrelated.Process.Signal(syscall.Signal(0)))
	time.Sleep(2500 * time.Millisecond)
	require.NoFileExists(t, survived)
}

func TestCancellationBeforeSupervisorStartsPreventsLateLaunch(t *testing.T) {
	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}

	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	stopped := make(chan error, 1)
	go func() {
		stopped <- cancelRemoteBenchmark(ctx, runner, remoteDir)
	}()

	require.Eventually(t, func() bool {
		state, err := os.ReadFile(benchmarkControlPath(remoteDir))
		return err == nil && strings.TrimSpace(string(state)) == "cancel"
	}, 5*time.Second, 20*time.Millisecond)

	started := filepath.Join(root, "started")
	err := runner.Run(ctx, supervisedBenchmarkCommand(remoteDir, "touch -- "+shellQuote(started)), io.Discard, io.Discard)
	require.Error(t, err)
	require.NoError(t, <-stopped)
	require.NoFileExists(t, started)
}

func TestCancellationControlLockWaitIsBounded(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	ready := filepath.Join(root, "locked")
	locker := exec.Command("python3", "-c", `import fcntl, sys, time
with open(sys.argv[1], "r+") as control:
    fcntl.flock(control, fcntl.LOCK_EX)
    with open(sys.argv[2], "w") as ready:
        ready.write("locked")
    time.sleep(5)
`, benchmarkControlPath(remoteDir), ready)
	require.NoError(t, locker.Start())
	t.Cleanup(func() {
		_ = locker.Process.Kill()
		_ = locker.Wait()
	})
	require.Eventually(t, func() bool {
		_, err := os.Stat(ready)
		return err == nil
	}, 3*time.Second, 20*time.Millisecond)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	command := "python3 -c " + shellQuote(benchmarkCancelScript) + " " + shellQuote(benchmarkControlPath(remoteDir)) + " 0.5"
	var stderr bytes.Buffer
	err := runner.Run(ctx, command, io.Discard, &stderr)
	require.Error(t, err)
	require.NoError(t, ctx.Err())
	require.Contains(t, stderr.String(), "benchmark control lock is unavailable")
}

func TestSupervisorTerminationSafety(t *testing.T) {
	end := strings.Index(benchmarkSupervisorScript, "\nwith open(sys.argv[1]")
	require.NotEqual(t, -1, end)
	script := benchmarkSupervisorScript[:end] + `
original_has_live_members = has_live_members
signals = []
signal_group = lambda child, sig: signals.append(sig) or True
has_live_members = lambda child: False
class Child:
    pid = os.getpgrp()
    def wait(self):
        return 0
stop_group(Child())
assert signals == [signal.SIGTERM, signal.SIGKILL], signals
sys.platform = "unsupported"
try:
    original_has_live_members(Child())
except RuntimeError:
    pass
else:
    raise AssertionError("unsupported platform must fail closed")
`
	output, err := exec.Command("python3", "-c", script).CombinedOutput()
	require.NoError(t, err, string(output))
}

func TestSupervisorDoesNotConfirmWhileProcessGroupRemainsAlive(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	ready := filepath.Join(root, "benchmark.pid")
	childScript := `import os, sys, time
with open(sys.argv[1], "w") as f:
    f.write(str(os.getpid()))
time.sleep(30)
`
	command := "exec python3 -c " + shellQuote(childScript) + " " + shellQuote(ready)
	supervisor := strings.Replace(benchmarkSupervisorScript, "os.killpg(child.pid, sig)", "None", 1)
	require.NotEqual(t, benchmarkSupervisorScript, supervisor)
	supervisorCommand := "python3 -c " + shellQuote(supervisor) + " " + shellQuote(benchmarkControlPath(remoteDir)) + " " + shellQuote(command)
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	done := make(chan error, 1)
	finished := make(chan struct{})
	supervisorLogPath := filepath.Join(root, "supervisor.log")
	supervisorLog, err := os.OpenFile(supervisorLogPath, os.O_CREATE|os.O_RDWR, 0600)
	require.NoError(t, err)
	go func() {
		defer close(finished)
		done <- runner.Run(ctx, supervisorCommand, supervisorLog, supervisorLog)
	}()
	t.Cleanup(func() {
		cancel()
		if data, readErr := os.ReadFile(ready); readErr == nil {
			if pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data))); parseErr == nil && pid > 0 {
				if group, groupErr := syscall.Getpgid(pid); groupErr == nil && group == pid {
					_ = syscall.Kill(-pid, syscall.SIGKILL)
				}
			}
		}
		select {
		case <-finished:
			_ = supervisorLog.Close()
		case <-time.After(3 * time.Second):
			t.Error("supervisor did not exit after cancellation")
		}
	})
	waitForPID(t, ready)

	cancelCommand := "python3 -c " + shellQuote(benchmarkCancelScript) + " " + shellQuote(benchmarkControlPath(remoteDir)) + " 5"
	var stderr bytes.Buffer
	err = runner.Run(ctx, cancelCommand, io.Discard, &stderr)
	require.Error(t, err)
	require.Contains(t, stderr.String(), "benchmark supervisor did not confirm termination")
	select {
	case err := <-done:
		require.Error(t, err)
		supervisorStderr, readErr := os.ReadFile(supervisorLogPath)
		require.NoError(t, readErr)
		require.Contains(t, string(supervisorStderr), "benchmark process group is still running")
	case <-time.After(time.Second):
		t.Fatal("supervisor did not report the live process group")
	}
	state, err := os.ReadFile(benchmarkControlPath(remoteDir))
	require.NoError(t, err)
	require.Equal(t, "cancel", strings.TrimSpace(string(state)))
}

func TestBenchmarkControlFailsClosed(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	require.Error(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

	started := filepath.Join(root, "unexpected-start")
	missingDir := filepath.Join(root, "missing")
	err := runner.Run(context.Background(), supervisedBenchmarkCommand(missingDir, "touch -- "+shellQuote(started)), io.Discard, io.Discard)
	require.Error(t, err)
	require.NoFileExists(t, started)
}

func TestRemoteRootIsCreatedExclusively(t *testing.T) {
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.Mkdir(existing, 0700))
	marker := filepath.Join(existing, "existing-data")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0600))

	runner := localCommandRunner{}
	require.Error(t, prepareRemoteRoot(context.Background(), runner, existing))
	data, err := os.ReadFile(marker)
	require.NoError(t, err)
	require.Equal(t, "keep", string(data))
	require.NoError(t, prepareRemoteRoot(context.Background(), runner, filepath.Join(root, "new")))
}

type rootCollisionRunner struct{ runner *fakeBenchRunner }

func (r rootCollisionRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if strings.HasPrefix(command, "umask 077 && mkdir -- ") && !strings.Contains(command, "set -C") {
		r.runner.commands = append(r.runner.commands, command)
		return fmt.Errorf("directory already exists")
	}

	return r.runner.Run(ctx, command, stdout, stderr)
}

func TestRunDoesNotRemoveUnownedRemoteRoot(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	runner := &fakeBenchRunner{}
	output, err := run(context.Background(), dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
		resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
			return BenchTarget{BaseURL: "http://router/default/chat", runner: rootCollisionRunner{runner: runner}}, nil
		},
	}, validOptions(t.TempDir()))
	require.ErrorContains(t, err, "directory already exists")
	manifestBytes, err := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, err)
	require.Contains(t, string(manifestBytes), "directory already exists")
	for _, command := range runner.commands {
		require.False(t, strings.HasPrefix(command, "rm -rf -- "), "unowned directory must not be removed")
	}
}

func TestRunCaseDoesNotRemoveUnownedDirectory(t *testing.T) {
	remoteRoot := t.TempDir()
	benchmarkCase := Case{InputLen: 1, OutputLen: 1, Concurrency: 1}
	remoteDir := benchmarkCaseRemoteResultDirectory(remoteRoot, benchmarkCase)
	require.NoError(t, os.Mkdir(remoteDir, 0700))
	marker := filepath.Join(remoteDir, "existing-data")
	require.NoError(t, os.WriteFile(marker, []byte("keep"), 0600))

	err := runCase(context.Background(), localCommandRunner{}, BenchTarget{}, Options{NumPrompts: 1, RequestRate: "inf"},
		benchmarkCase, t.TempDir(), remoteRoot)
	require.ErrorIs(t, err, errRemoteArtifactsPreserved)
	data, readErr := os.ReadFile(marker)
	require.NoError(t, readErr)
	require.Equal(t, "keep", string(data))
}

func TestControlledBenchmarkNormalAndFailedExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		wantErr bool
	}{
		{name: "success", command: "exit 0"},
		{name: "failure", command: "exit 7", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remoteDir := filepath.Join(t.TempDir(), "case")
			runner := localCommandRunner{}
			require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

			err := runControlledBenchmark(context.Background(), runner, remoteDir, tc.command, io.Discard, io.Discard)
			if tc.wantErr {
				require.Error(t, err)
				require.NotErrorIs(t, err, errRemoteTerminationUnconfirmed)
			} else {
				require.NoError(t, err)
			}
			state, readErr := os.ReadFile(benchmarkControlPath(remoteDir))
			require.NoError(t, readErr)
			require.Equal(t, "finished", strings.TrimSpace(string(state)))
		})
	}
}

func TestControlledBenchmarkStopsDescendantWhenLeaderExits(t *testing.T) {
	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	ready := filepath.Join(root, "descendant-ready")
	survived := filepath.Join(root, "descendant-survived")
	script := `import os, subprocess, sys, time
child = '''import signal, sys, time
signal.signal(signal.SIGTERM, signal.SIG_IGN)
with open(sys.argv[1], "w") as f:
    f.write("ready")
time.sleep(4)
with open(sys.argv[2], "w") as f:
    f.write("still running")
'''
subprocess.Popen([sys.executable, "-c", child, sys.argv[1], sys.argv[2]])
while not os.path.exists(sys.argv[1]):
    time.sleep(0.01)
`
	command := "exec python3 -c " + shellQuote(script) + " " + shellQuote(ready) + " " + shellQuote(survived)
	require.NoError(t, runControlledBenchmark(context.Background(), runner, remoteDir, command, io.Discard, io.Discard))
	time.Sleep(2500 * time.Millisecond)
	require.NoFileExists(t, survived)
}

type cancelLifecycleRunner struct {
	started chan struct{}
	stopped chan struct{}
	stopErr error
	mu      sync.Mutex
	events  []string
}

func (r *cancelLifecycleRunner) record(event string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, event)
}

func (r *cancelLifecycleRunner) Run(ctx context.Context, command string, stdout, _ io.Writer) error {
	switch {
	case command == benchmarkPreflightCommand():
		return nil
	case strings.Contains(command, "# neutree-bench-supervisor"):
		r.record("start")
		close(r.started)
		select {
		case <-r.stopped:
			return fmt.Errorf("supervisor canceled")
		case <-ctx.Done():
			return ctx.Err()
		}
	case strings.Contains(command, "# neutree-bench-cancel"):
		r.record("stop")
		if ctx.Err() != nil {
			return fmt.Errorf("cleanup context is already canceled")
		}
		if r.stopErr != nil {
			return r.stopErr
		}
		close(r.stopped)
		return nil
	case strings.HasPrefix(command, "python3 -c "):
		_, _ = io.WriteString(stdout, `{"data":[{"name":"router-model"}]}`)
		return nil
	case strings.Contains(command, "set -C"):
		r.record("prepare")
		return nil
	case strings.HasPrefix(command, "umask 077 && mkdir -- "):
		r.record("root")
		return nil
	case strings.HasPrefix(command, "rm -rf -- "):
		r.record("remove")
		return nil
	default:
		return fmt.Errorf("unexpected remote command")
	}
}

func TestControlledBenchmarkStopsOnDeadline(t *testing.T) {
	runner := &cancelLifecycleRunner{started: make(chan struct{}), stopped: make(chan struct{})}
	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runControlledBenchmark(ctx, runner, "/tmp/bench-case", "benchmark", io.Discard, io.Discard)
	}()
	select {
	case <-runner.started:
	case <-time.After(2 * time.Second):
		t.Fatal("benchmark did not start")
	}
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.DeadlineExceeded)
	case <-time.After(3 * time.Second):
		t.Fatal("deadline did not stop benchmark")
	}
	require.Contains(t, runner.events, "stop")
}

type stalledExecutionRunner struct {
	started chan struct{}
	release chan struct{}
}

func (r stalledExecutionRunner) Run(_ context.Context, command string, _, _ io.Writer) error {
	if strings.Contains(command, "# neutree-bench-supervisor") {
		close(r.started)
		<-r.release
	}

	return nil
}

func TestControlledBenchmarkBoundsStalledStreamShutdown(t *testing.T) {
	runner := stalledExecutionRunner{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(runner.release) })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runControlledBenchmark(ctx, runner, "/tmp/bench-case", "benchmark", io.Discard, io.Discard)
	}()
	<-runner.started
	cancel()
	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, errRemoteTerminationUnconfirmed)
		require.ErrorContains(t, err, "remote execution stream did not close")
	case <-time.After(5 * time.Second):
		t.Fatal("stream shutdown was not bounded")
	}
}

type cancelAfterResultRunner struct {
	runner *fakeBenchRunner
	cancel context.CancelFunc
}

type cancelDuringResultRunner struct {
	runner *fakeBenchRunner
	cancel context.CancelFunc
}

func (r cancelDuringResultRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if strings.HasPrefix(command, "cat -- ") {
		r.runner.commands = append(r.runner.commands, command)
		_, _ = io.WriteString(stdout, `{"partial":`)
		r.cancel()
		return ctx.Err()
	}

	return r.runner.Run(ctx, command, stdout, stderr)
}

func TestRunCancellationDuringResultCopyRetainsRemoteAndPartialResult(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeBenchRunner{}
	output, err := run(ctx, dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
		resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
			return BenchTarget{BaseURL: "http://router/default/chat", runner: cancelDuringResultRunner{runner: runner, cancel: cancel}}, nil
		},
	}, validOptions(t.TempDir()))
	require.ErrorIs(t, err, context.Canceled)
	require.ErrorIs(t, err, errRemoteArtifactsPreserved)

	caseDir := filepath.Join(output, "input-16-output-4-concurrency-2")
	require.NoFileExists(t, filepath.Join(caseDir, benchmarkResultFileName))
	partial, err := os.ReadFile(filepath.Join(caseDir, benchmarkResultFileName+".partial"))
	require.NoError(t, err)
	require.Equal(t, `{"partial":`, string(partial))

	manifestBytes, err := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, err)
	var got manifest
	require.NoError(t, json.Unmarshal(manifestBytes, &got))
	require.Len(t, got.Cases, 1)
	require.Equal(t, benchmarkCanceledStatus, got.Cases[0].Status)
	for _, command := range runner.commands {
		require.False(t, strings.HasPrefix(command, "rm -rf -- "), "remote results must be retained")
	}
}

func (r cancelAfterResultRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	err := r.runner.Run(ctx, command, stdout, stderr)
	if strings.HasPrefix(command, "rm -rf -- ") && strings.Contains(command, "input-") {
		r.cancel()
	}

	return err
}

func TestRunCancellationBetweenCasesDoesNotStartAnotherCase(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	runner := &fakeBenchRunner{}
	options := validOptions(t.TempDir())
	options.Concurrencies = []int{1, 2}

	output, err := run(ctx, dependencies{
		resources: fakeResourceGetter{resources: map[string]json.RawMessage{
			resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
			resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
		}},
		clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
		resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
			return BenchTarget{BaseURL: "http://router/default/chat", runner: cancelAfterResultRunner{runner: runner, cancel: cancel}}, nil
		},
	}, options)
	require.ErrorIs(t, err, context.Canceled)

	manifestBytes, err := os.ReadFile(filepath.Join(output, manifestFileName))
	require.NoError(t, err)
	var got manifest
	require.NoError(t, json.Unmarshal(manifestBytes, &got))
	require.Len(t, got.Cases, 1)
	require.Equal(t, benchmarkSucceededStatus, got.Cases[0].Status)
	require.Contains(t, got.Error, "context canceled")

	var started int
	for _, command := range runner.commands {
		if strings.Contains(command, "# neutree-bench-supervisor") {
			started++
		}
	}
	require.Equal(t, 1, started)
}

func TestRunCancellationStopsBeforeCleaningAndPreservesManifest(t *testing.T) {
	for _, tc := range []struct {
		name            string
		stopErr         error
		wantRemove      bool
		wantUnconfirmed bool
	}{
		{name: "confirmed", wantRemove: true},
		{name: "unconfirmed", stopErr: fmt.Errorf("exec unavailable"), wantUnconfirmed: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			endpoint, cluster, credentialCluster := benchmarkFixtures()
			runner := &cancelLifecycleRunner{started: make(chan struct{}), stopped: make(chan struct{}), stopErr: tc.stopErr}
			deps := dependencies{
				resources: fakeResourceGetter{resources: map[string]json.RawMessage{
					resourceKey("Endpoint", "default", "chat"):     mustJSON(t, endpoint),
					resourceKey("Cluster", "default", "cluster-a"): mustJSON(t, cluster),
				}},
				clusterCredentials: fakeClusterCredentials{cluster: &credentialCluster},
				resolveTarget: func(context.Context, *v1.Endpoint, *v1.Cluster, *v1.Cluster) (BenchTarget, error) {
					return BenchTarget{BaseURL: "http://router/default/chat", runner: runner}, nil
				},
			}
			options := validOptions(t.TempDir())
			options.Concurrencies = []int{1, 2}
			ctx, cancel := context.WithCancel(context.Background())
			t.Cleanup(cancel)
			type result struct {
				output string
				err    error
			}
			done := make(chan result, 1)
			go func() {
				output, err := run(ctx, deps, options)
				done <- result{output: output, err: err}
			}()

			select {
			case <-runner.started:
			case <-time.After(3 * time.Second):
				t.Fatal("benchmark did not start")
			}
			cancel()
			var got result
			select {
			case got = <-done:
			case <-time.After(5 * time.Second):
				t.Fatal("cancellation did not complete")
			}

			require.ErrorIs(t, got.err, context.Canceled)
			if tc.wantUnconfirmed {
				require.ErrorIs(t, got.err, errRemoteTerminationUnconfirmed)
			}
			manifestBytes, err := os.ReadFile(filepath.Join(got.output, manifestFileName))
			require.NoError(t, err)
			var gotManifest manifest
			require.NoError(t, json.Unmarshal(manifestBytes, &gotManifest))
			require.Len(t, gotManifest.Cases, 1)
			require.Equal(t, "Canceled", gotManifest.Cases[0].Status)
			require.FileExists(t, filepath.Join(got.output, "input-16-output-4-concurrency-1", benchmarkStdoutFileName))
			require.Greater(t, indexOfEvent(runner.events, "stop"), indexOfEvent(runner.events, "start"))
			if tc.wantRemove {
				require.Greater(t, indexOfEvent(runner.events, "remove"), indexOfEvent(runner.events, "stop"))
			} else {
				require.NotContains(t, runner.events, "remove")
			}
		})
	}
}

func indexOfEvent(events []string, target string) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}

	return -1
}
