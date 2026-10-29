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

	// remoteLeaseTimeout is how long the monitor tolerates a stale renewal.
	// remoteRenewInterval keeps the lease fresh while the CLI is alive, so three
	// consecutive failed renewals, a killed CLI, or a suspended laptop stop the
	// benchmark instead of leaving it running on the GPU.
	remoteLeaseTimeout  = 60 * time.Second
	remoteRenewInterval = 10 * time.Second
	remoteRenewTimeout  = 5 * time.Second
)

// remoteStopGrace bounds how long the CLI waits for the monitor to confirm it
// stopped the benchmark after a cancellation request.
var remoteStopGrace = 10 * time.Second

var errRemoteTerminationUnconfirmed = errors.New("remote benchmark termination not confirmed")

// The monitor owns the benchmark's process group and renews nothing itself: it
// stops the benchmark when the CLI writes the cancel file or stops renewing the
// lease. A canceled run exits 130, an unresponsive process group exits 137.
//
// The benchmark writes its tqdm progress bar into the same log as its regular
// output, so the CLI can tail it and show completed requests while it runs.
const benchmarkMonitorScript = `# neutree-bench-monitor
import os
import shutil
import signal
import subprocess
import sys
import time

renew, cancel, log, result_dir = sys.argv[1:5]
lease = float(sys.argv[5])
command = sys.argv[6]

if os.path.exists(cancel):
    sys.exit(130)

open(renew, "w").close()
log_file = open(log, "wb")
child = subprocess.Popen(
    ["sh", "-c", command], stdout=log_file, stderr=subprocess.STDOUT, start_new_session=True
)

def kill_group(sig):
    try:
        os.killpg(child.pid, sig)
    except ProcessLookupError:
        return False
    return True

def stop():
    if not kill_group(signal.SIGTERM):
        return child.poll() is not None
    deadline = time.monotonic() + 2
    while child.poll() is None and time.monotonic() < deadline:
        time.sleep(0.05)
    if child.poll() is None:
        kill_group(signal.SIGKILL)
        deadline = time.monotonic() + 2
        while child.poll() is None and time.monotonic() < deadline:
            time.sleep(0.05)
    return child.poll() is not None

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
        sys.exit(137)
    if expired and not os.path.exists(os.path.join(result_dir, "result.json")):
        shutil.rmtree(result_dir, ignore_errors=True)
    sys.exit(130)

kill_group(signal.SIGKILL)
code = child.returncode
sys.exit(code if code >= 0 else 128 - code)
`

func benchmarkRenewPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkRenewFileName))
}

func benchmarkCancelPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkCancelFileName))
}

func prepareRemoteRoot(ctx context.Context, runner remoteRunner, remoteRoot string) error {
	command := "umask 077 && mkdir -- " + shellQuote(remoteRoot)
	if err := runner.Run(ctx, command, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("create exclusive remote benchmark directory: %w", err)
	}

	return nil
}

// prepareBenchmarkControl creates the case directory exclusively, so the
// benchmark never writes into a directory it does not own.
func prepareBenchmarkControl(ctx context.Context, runner remoteRunner, remoteDir string) error {
	command := "umask 077 && mkdir -- " + shellQuote(remoteDir)
	if err := runner.Run(ctx, command, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("prepare remote benchmark directory: %w", err)
	}

	return nil
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

// runControlledBenchmark runs one case under the remote monitor. The execution
// stream is not tied to ctx, because the monitor must stay reachable until it
// confirms that the benchmark process group is gone.
func runControlledBenchmark(ctx context.Context, runner remoteRunner, remoteDir, command string, diagnostics io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	runCtx, cancelRun := context.WithCancel(context.WithoutCancel(ctx))
	defer cancelRun()

	done := make(chan error, 1)

	go func() {
		done <- runner.Run(runCtx, benchmarkMonitorCommand(remoteDir, command, remoteLeaseTimeout), io.Discard, diagnostics)
	}()

	select {
	case runErr := <-done:
		if ctx.Err() == nil {
			return runErr
		}
	case <-ctx.Done():
	}

	if err := requestRemoteCancel(runner, remoteDir); err != nil {
		klog.Warningf("%v; the remote lease stops the benchmark within %s", err, remoteLeaseTimeout)
	}

	select {
	case <-done:
	case <-time.After(remoteStopGrace):
		return fmt.Errorf("%w: %w", errRemoteTerminationUnconfirmed, ctx.Err())
	}

	return ctx.Err()
}
