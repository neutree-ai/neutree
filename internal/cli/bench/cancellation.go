package bench

import (
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"time"

	"k8s.io/klog/v2"
)

const (
	benchmarkRenewFileName  = ".neutree-bench-renew"
	benchmarkCancelFileName = ".neutree-bench-cancel"
	benchmarkLogFileName    = "benchmark.log"

	// remoteLeaseTimeout is how long the monitor tolerates a stale renewal.
	// remoteRenewInterval keeps the lease fresh while the CLI is alive, so three
	// consecutive failed renewals, a killed CLI, or a suspended laptop stop the
	// benchmark instead of leaving it running on the GPU.
	remoteLeaseTimeout  = 60 * time.Second
	remoteRenewInterval = 10 * time.Second
	remoteRenewTimeout  = 5 * time.Second

	// benchmarkMonitorStopFailureExitCode is how the monitor reports a process
	// group that survived SIGKILL escalation. A canceled run exits 130, which is
	// the expected outcome of a cancellation.
	benchmarkMonitorStopFailureExitCode = 137
)

// remoteStopGrace bounds how long the CLI waits for the monitor to confirm it
// stopped the benchmark after a cancellation request.
var remoteStopGrace = 10 * time.Second

var errRemoteTerminationUnconfirmed = errors.New("remote benchmark termination not confirmed")

// The monitor owns the benchmark's process group and renews nothing itself: it
// stops the benchmark when the CLI writes the cancel file or stops renewing the
// lease. A canceled run exits 130, an unresponsive process group exits 137.
//
// The monitor relays the benchmark output on its own stdout, which is the
// stream the CLI already holds open, so the live log needs no second remote
// process to track down when the CLI is interrupted. The command it runs starts
// with `exec`, so the benchmark replaces the shell and the monitor is its
// parent: it reaps the benchmark itself instead of leaving it to the
// container's PID 1, which never reaps.
const benchmarkMonitorScript = `# neutree-bench-monitor
import os
import select
import shutil
import signal
import subprocess
import sys
import threading
import time

renew, cancel, log, result_dir = sys.argv[1:5]
lease = float(sys.argv[5])
command = sys.argv[6]

if os.path.exists(cancel):
    sys.exit(130)

def prctl(option, value):
    try:
        import ctypes
        ctypes.CDLL(None).prctl(option, value, 0, 0, 0)
    except Exception:
        pass

# Benchmark descendants that outlive their parent re-parent to the monitor
# rather than to a PID 1 that does not reap them, so nothing is left defunct.
prctl(36, 1)  # PR_SET_CHILD_SUBREAPER
# A canceled exec session kills the monitor; the benchmark must not keep
# running without the process that enforces its lease.
def die_with_monitor():
    prctl(1, signal.SIGKILL)  # PR_SET_PDEATHSIG

open(renew, "w").close()
log_file = open(log, "wb")
child = subprocess.Popen(
    ["sh", "-c", command],
    stdout=subprocess.PIPE,
    stderr=subprocess.STDOUT,
    start_new_session=True,
    preexec_fn=die_with_monitor,
)

def relay():
    fd = child.stdout.fileno()
    while True:
        if not select.select([fd], [], [], 0.2)[0]:
            # A benchmark descendant can keep the pipe open after the benchmark
            # itself exited; stop instead of waiting for a write end that may
            # never close.
            if child.poll() is not None:
                return
            continue
        data = os.read(fd, 65536)
        if not data:
            return
        log_file.write(data)
        log_file.flush()
        try:
            sys.stdout.buffer.write(data)
            sys.stdout.buffer.flush()
        except (BrokenPipeError, OSError, ValueError):
            pass  # the CLI is gone; the log file still records the run

relay_thread = threading.Thread(target=relay, daemon=True)
relay_thread.start()

# A group id stays in use while any member of the group is alive, so an id this
# monitor cannot signal has already been recycled by another process: nothing of
# ours is left to stop. macOS reports that as EPERM rather than ESRCH.
def signal_group(sig):
    try:
        os.killpg(child.pid, sig)
    except (ProcessLookupError, PermissionError):
        return False
    return True

def reap():
    # Descendants re-parent to this monitor as subreaper. A reaped leader or
    # descendant stops counting as a member of the group.
    while True:
        try:
            pid, _ = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            return
        if pid == 0:
            return

def group_alive():
    return signal_group(0)

def wait_for_group(seconds):
    deadline = time.monotonic() + seconds
    while True:
        reap()
        if not group_alive():
            return True
        if time.monotonic() >= deadline:
            return False
        time.sleep(0.05)

def stop():
    if not signal_group(signal.SIGTERM):
        return True
    # The benchmark itself can exit while a descendant that ignored SIGTERM
    # keeps the group alive, so the group rather than the direct child decides
    # whether stopping succeeded.
    if wait_for_group(2):
        return True
    signal_group(signal.SIGKILL)
    return wait_for_group(2)

def finish(code):
    relay_thread.join(2)
    deadline = time.monotonic() + 0.5
    while time.monotonic() < deadline:
        try:
            pid, _ = os.waitpid(-1, os.WNOHANG)
        except ChildProcessError:
            break
        if pid == 0:
            time.sleep(0.02)
    sys.exit(code)

# A canceled case is retained for the CLI to inspect. A case stopped by the
# lease is not: the lease only expires when the CLI is gone, so nothing will
# ever collect the directory, and leaving it behind would leak another
# multi-megabyte result per interrupted run.
expired = False
while child.poll() is None:
    if os.path.exists(cancel):
        break
    if time.time() - os.stat(renew).st_mtime > lease:
        expired = True
        break
    time.sleep(0.2)

if child.poll() is None:
    if not stop():
        sys.stderr.write("neutree-bench: benchmark process group did not stop\n")
        sys.stderr.flush()
        finish(137)
    if expired and not os.path.exists(os.path.join(result_dir, "result.json")):
        shutil.rmtree(result_dir, ignore_errors=True)
    finish(130)

# The benchmark exited on its own; any descendant still holding the group is
# leftover work that must not outlive the case the monitor reports on.
signal_group(signal.SIGKILL)
wait_for_group(2)
code = child.returncode
finish(code if code >= 0 else 128 - code)
`

func benchmarkRenewPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkRenewFileName))
}

func benchmarkCancelPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkCancelFileName))
}

func benchmarkLogPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkLogFileName))
}

// renewRemoteLease keeps the monitor's lease fresh until the returned stop
// function runs.
func renewRemoteLease(ctx context.Context, runner remoteRunner, renewPath string, interval time.Duration) func() {
	renewCtx, cancel := context.WithCancel(ctx)
	done := make(chan struct{})

	go func() {
		defer close(done)

		ticker := time.NewTicker(interval)
		defer ticker.Stop()

		for {
			select {
			case <-renewCtx.Done():
				return
			case <-ticker.C:
			}

			renewCtx, cancelRenew := context.WithTimeout(context.Background(), remoteRenewTimeout)
			err := runner.Run(renewCtx, benchmarkRenewCommand(renewPath), io.Discard, io.Discard)

			cancelRenew()

			if err != nil {
				klog.Warningf("failed to renew remote benchmark lease %s: %v", renewPath, err)
			}
		}
	}()

	return func() {
		cancel()
		<-done
	}
}

// requestRemoteCancel asks the monitor to stop. A failed request is not fatal:
// the lease expires and the monitor stops the benchmark on its own.
func requestRemoteCancel(runner remoteRunner, remoteDir string) error {
	ctx, cancel := context.WithTimeout(context.Background(), remoteCleanupTimeout)
	defer cancel()

	if err := runner.Run(ctx, benchmarkCancelCommand(benchmarkCancelPath(remoteDir)), io.Discard, io.Discard); err != nil {
		return fmt.Errorf("request remote benchmark cancellation: %w", err)
	}

	return nil
}

// runControlledBenchmark runs one case under the remote monitor and writes
// everything the monitor relays to output. The execution stream is not tied to
// ctx, because the monitor must stay reachable until it confirms that the
// benchmark process group is gone.
func runControlledBenchmark(ctx context.Context, runner remoteRunner, remoteDir, command string, output io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()

	done := make(chan error, 1)

	go func() {
		done <- runner.Run(runCtx, benchmarkMonitorCommand(remoteDir, command, remoteLeaseTimeout), output, output)
	}()

	finished := false

	var monitorErr error

	select {
	case monitorErr = <-done:
		finished = true

		if ctx.Err() == nil {
			return monitorErr
		}
	case <-ctx.Done():
	}

	if err := requestRemoteCancel(runner, remoteDir); err != nil {
		klog.Warningf("%v; the remote lease stops the benchmark within %s", err, remoteLeaseTimeout)
	}

	// A stream that already returned has nothing left to confirm; waiting on
	// done again would stall for the full grace period and report a failure
	// that did not happen.
	if !finished {
		select {
		case monitorErr = <-done:
		case <-time.After(remoteStopGrace):
			return fmt.Errorf("%w: %w", errRemoteTerminationUnconfirmed, ctx.Err())
		}
	}

	// A normal cancellation ends with the monitor's 130. Any other exit means
	// the benchmark may still be running on the GPU, and the user has to know
	// that instead of reading a plain "canceled".
	if code, ok := remoteExitCode(monitorErr); ok && code == benchmarkMonitorStopFailureExitCode {
		return fmt.Errorf("%w; the remote benchmark process group did not stop", ctx.Err())
	}

	return ctx.Err()
}

// remoteExitCode returns the exit status a failed remote command reported, when
// the runner preserved it. A Kubernetes exec stream reports it as ExitStatus,
// and an SSH command reports the remote status through the local ssh process.
func remoteExitCode(err error) (int, bool) {
	var execErr interface{ ExitStatus() int }

	if errors.As(err, &execErr) {
		return execErr.ExitStatus(), true
	}

	var exitErr interface{ ExitCode() int }

	if errors.As(err, &exitErr) {
		return exitErr.ExitCode(), true
	}

	return 0, false
}
