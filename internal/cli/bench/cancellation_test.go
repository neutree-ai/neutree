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

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(data []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.Write(data)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()

	return b.buf.String()
}

func requirePython(t *testing.T) {
	t.Helper()

	if _, err := exec.LookPath("python3"); err != nil {
		t.Skip("python3 is required")
	}
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
	requirePython(t)

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
		done <- runControlledBenchmark(ctx, runner, remoteDir, command, io.Discard)
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
	case <-time.After(15 * time.Second):
		t.Fatal("canceled benchmark did not stop")
	}

	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
	require.NoError(t, unrelated.Process.Signal(syscall.Signal(0)))
	time.Sleep(2500 * time.Millisecond)
	require.NoFileExists(t, survived)
}

func TestMonitorStopsBenchmarkWhenLeaseExpires(t *testing.T) {
	requirePython(t)

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

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- runner.Run(ctx, benchmarkMonitorCommand(remoteDir, command, time.Second), io.Discard, io.Discard)
	}()

	pid := waitForPID(t, ready)

	select {
	case err := <-done:
		require.Error(t, err)
		require.NoDirExists(t, remoteDir)
	case <-time.After(12 * time.Second):
		t.Fatal("monitor did not stop the benchmark after the lease expired")
	}

	require.ErrorIs(t, syscall.Kill(pid, 0), syscall.ESRCH)
}

func TestMonitorSkipsBenchmarkWhenCancellationWasRequested(t *testing.T) {
	requirePython(t)

	root := t.TempDir()
	remoteDir := filepath.Join(root, "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	require.NoError(t, runner.Run(
		context.Background(),
		benchmarkCancelCommand(benchmarkCancelPath(remoteDir)),
		io.Discard,
		io.Discard,
	))

	started := filepath.Join(root, "started")
	err := runner.Run(
		context.Background(),
		benchmarkMonitorCommand(remoteDir, "touch -- "+shellQuote(started), remoteLeaseTimeout),
		io.Discard,
		io.Discard,
	)
	require.Error(t, err)
	require.NoFileExists(t, started)
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
	require.NoError(t, runControlledBenchmark(context.Background(), runner, remoteDir, command, io.Discard))
	time.Sleep(2500 * time.Millisecond)
	require.NoFileExists(t, survived)
}

func TestControlledBenchmarkNormalAndFailedExit(t *testing.T) {
	for _, tc := range []struct {
		name    string
		command string
		wantErr bool
	}{
		{name: "success", command: "echo benchmark output"},
		{name: "failure", command: "echo benchmark failure && exit 7", wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			remoteDir := filepath.Join(t.TempDir(), "case")
			runner := localCommandRunner{}
			require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

			err := runControlledBenchmark(context.Background(), runner, remoteDir, tc.command, io.Discard)
			if tc.wantErr {
				require.Error(t, err)
				require.NotErrorIs(t, err, errRemoteTerminationUnconfirmed)
			} else {
				require.NoError(t, err)
			}

			log, readErr := os.ReadFile(benchmarkLogPath(remoteDir))
			require.NoError(t, readErr)
			require.Contains(t, string(log), "benchmark")
		})
	}
}

// vllm bench draws its progress bar as carriage-return frames. They have to
// reach the log byte for byte, because the CLI renders them on the terminal
// from the tailed log.
func TestMonitorKeepsProgressFramesInTheLog(t *testing.T) {
	remoteDir := filepath.Join(t.TempDir(), "case")
	runner := localCommandRunner{}
	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))

	command := `printf 'progress 1/3\rprogress 2/3\rprogress 3/3\r'; echo finished`
	require.NoError(t, runControlledBenchmark(context.Background(), runner, remoteDir, command, io.Discard))

	log, err := os.ReadFile(benchmarkLogPath(remoteDir))
	require.NoError(t, err)
	require.Contains(t, string(log), "progress 1/3\rprogress 2/3\rprogress 3/3\r")
	require.Contains(t, string(log), "finished")
}

func TestRenewRemoteLeaseStampsUntilStopped(t *testing.T) {
	renewPath := filepath.Join(t.TempDir(), benchmarkRenewFileName)
	runner := &fakeBenchRunner{}
	renewals := func() int {
		return countBenchCommands(runner.snapshot(), func(command string) bool {
			return command == benchmarkRenewCommand(renewPath)
		})
	}

	stop := renewRemoteLease(context.Background(), runner, renewPath, 10*time.Millisecond)
	require.Eventually(t, func() bool { return renewals() >= 2 }, 3*time.Second, 10*time.Millisecond)
	stop()

	count := renewals()
	time.Sleep(50 * time.Millisecond)
	require.Equal(t, count, renewals())
}

func TestTailRemoteLogStreamsAppendedLines(t *testing.T) {
	logPath := filepath.Join(t.TempDir(), benchmarkLogFileName)
	require.NoError(t, os.WriteFile(logPath, []byte("first line\n"), 0600))

	output := &lockedBuffer{}
	stop := tailRemoteLog(context.Background(), localCommandRunner{}, logPath, output)
	defer stop()

	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), "first line")
	}, 5*time.Second, 50*time.Millisecond)

	file, err := os.OpenFile(logPath, os.O_APPEND|os.O_WRONLY, 0600)
	require.NoError(t, err)
	_, err = file.WriteString("second line\n")
	require.NoError(t, err)
	require.NoError(t, file.Close())

	require.Eventually(t, func() bool {
		return strings.Contains(output.String(), "second line")
	}, 5*time.Second, 50*time.Millisecond)
}

func TestPrepareBenchmarkControlCreatesDirectoryExclusively(t *testing.T) {
	remoteDir := filepath.Join(t.TempDir(), "case")
	runner := localCommandRunner{}

	require.NoError(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
	require.Error(t, prepareBenchmarkControl(context.Background(), runner, remoteDir))
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

// rootCollisionRunner fails the run root creation, which is the only mkdir that
// does not target a case directory.
type rootCollisionRunner struct{ runner *fakeBenchRunner }

func (r rootCollisionRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if strings.HasPrefix(command, "umask 077 && mkdir -- ") && !strings.Contains(command, "input-") {
		r.runner.record(command)

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
	require.ErrorContains(t, err, "prepare remote benchmark directory")

	data, readErr := os.ReadFile(marker)
	require.NoError(t, readErr)
	require.Equal(t, "keep", string(data))
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
	case strings.Contains(command, "# neutree-bench-monitor"):
		r.record("start")
		close(r.started)

		select {
		case <-r.stopped:
			return fmt.Errorf("benchmark canceled")
		case <-ctx.Done():
			return ctx.Err()
		}
	case strings.HasPrefix(command, ": > "):
		r.record("stop")
		if ctx.Err() != nil {
			return fmt.Errorf("cleanup context is already canceled")
		}

		if r.stopErr != nil {
			return r.stopErr
		}

		close(r.stopped)

		return nil
	case strings.HasPrefix(command, "tail -n +1 -F -- "):
		<-ctx.Done()

		return ctx.Err()
	case strings.HasPrefix(command, "touch -- "):
		return nil
	case strings.HasPrefix(command, "python3 -c "):
		_, _ = io.WriteString(stdout, `{"data":[{"name":"router-model"}]}`)

		return nil
	case strings.HasPrefix(command, "umask 077 && mkdir -- "):
		r.record("prepare")

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
		done <- runControlledBenchmark(ctx, runner, "/tmp/bench-case", "benchmark", io.Discard)
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
	if strings.Contains(command, "# neutree-bench-monitor") {
		close(r.started)
		<-r.release
	}

	return nil
}

func TestControlledBenchmarkBoundsStalledStreamShutdown(t *testing.T) {
	previousGrace := remoteStopGrace
	remoteStopGrace = 200 * time.Millisecond
	t.Cleanup(func() { remoteStopGrace = previousGrace })

	runner := stalledExecutionRunner{started: make(chan struct{}), release: make(chan struct{})}
	t.Cleanup(func() { close(runner.release) })

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)

	go func() {
		done <- runControlledBenchmark(ctx, runner, "/tmp/bench-case", "benchmark", io.Discard)
	}()

	<-runner.started
	cancel()

	select {
	case err := <-done:
		require.ErrorIs(t, err, context.Canceled)
		require.ErrorIs(t, err, errRemoteTerminationUnconfirmed)
	case <-time.After(5 * time.Second):
		t.Fatal("stream shutdown was not bounded")
	}
}

type cancelDuringResultRunner struct {
	runner *fakeBenchRunner
	cancel context.CancelFunc
}

func (r cancelDuringResultRunner) Run(ctx context.Context, command string, stdout, stderr io.Writer) error {
	if strings.HasPrefix(command, "cat -- ") {
		r.runner.record(command)
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

type cancelAfterResultRunner struct {
	runner *fakeBenchRunner
	cancel context.CancelFunc
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

	require.Equal(t, 1, countBenchCommands(runner.commands, func(command string) bool {
		return strings.Contains(command, "# neutree-bench-monitor")
	}))
}

func TestRunCancellationStopsBeforeCleaningAndPreservesManifest(t *testing.T) {
	endpoint, cluster, credentialCluster := benchmarkFixtures()
	runner := &cancelLifecycleRunner{started: make(chan struct{}), stopped: make(chan struct{})}
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
	case <-time.After(15 * time.Second):
		t.Fatal("cancellation did not complete")
	}

	require.ErrorIs(t, got.err, context.Canceled)

	manifestBytes, err := os.ReadFile(filepath.Join(got.output, manifestFileName))
	require.NoError(t, err)

	var gotManifest manifest
	require.NoError(t, json.Unmarshal(manifestBytes, &gotManifest))
	require.Len(t, gotManifest.Cases, 1)
	require.Equal(t, benchmarkCanceledStatus, gotManifest.Cases[0].Status)
	require.FileExists(t, filepath.Join(got.output, "input-16-output-4-concurrency-1", benchmarkStdoutFileName))
	require.Greater(t, indexOfEvent(runner.events, "stop"), indexOfEvent(runner.events, "start"))
	require.NotContains(t, runner.events, "remove", "a canceled case must keep its remote directory")
}

func indexOfEvent(events []string, target string) int {
	for index, event := range events {
		if event == target {
			return index
		}
	}

	return -1
}
