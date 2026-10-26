package bench

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"time"

	"k8s.io/klog/v2"
)

const (
	benchmarkControlFileName    = ".neutree-bench-control"
	remoteStreamShutdownTimeout = 2 * time.Second
)

var (
	errRemoteTerminationUnconfirmed = errors.New("remote benchmark termination not confirmed")
	errRemoteArtifactsPreserved     = errors.New("remote benchmark directory retained for inspection")
)

// The supervisor retains ownership of the child until it has signaled the
// isolated process group, so its PID cannot be reused during termination.
const benchmarkSupervisorScript = `# neutree-bench-supervisor
import fcntl
import os
import signal
import subprocess
import sys
import time

def read_state(control):
    control.seek(0)
    return control.read().strip()

def write_state(control, value):
    control.seek(0)
    control.write(value + "\n")
    control.truncate()
    control.flush()

def signal_group(child, sig):
    try:
        os.killpg(child.pid, sig)
        return True
    except ProcessLookupError:
        return False
    except PermissionError:
        status = os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
        if status is None:
            raise
        child.wait()
        try:
            os.killpg(child.pid, 0)
        except ProcessLookupError:
            return False
        raise RuntimeError("cannot confirm benchmark process group termination")

def has_live_members(child):
    if sys.platform.startswith("linux"):
        with os.scandir("/proc") as entries:
            for entry in entries:
                if not entry.name.isdecimal():
                    continue
                try:
                    if os.getpgid(int(entry.name)) != child.pid:
                        continue
                    with open(os.path.join(entry.path, "stat")) as stat:
                        data = stat.read()
                except (FileNotFoundError, ProcessLookupError):
                    continue
                end = data.rfind(")")
                fields = data[end + 2:].split()
                if end < 0 or len(fields) < 3:
                    raise RuntimeError("invalid process stat")
                if fields[2] == str(child.pid) and fields[0] not in ("Z", "X"):
                    return True
        return False
    if sys.platform == "darwin":
        output = subprocess.check_output(["ps", "-A", "-o", "pid=", "-o", "pgid=", "-o", "state="], text=True)
        for line in output.splitlines():
            fields = line.split()
            if len(fields) != 3:
                raise RuntimeError("invalid process listing")
            if fields[1] == str(child.pid) and fields[2][0] not in ("Z", "X"):
                return True
        return False
    raise RuntimeError("cannot inspect benchmark process group on this platform")

def stop_group(child):
    if not signal_group(child, signal.SIGTERM):
        return child.wait()
    deadline = time.monotonic() + 2
    while has_live_members(child):
        if time.monotonic() >= deadline:
            break
        time.sleep(0.1)
    if not signal_group(child, signal.SIGKILL):
        return child.wait()
    deadline = time.monotonic() + 2
    while has_live_members(child):
        if time.monotonic() >= deadline:
            raise TimeoutError("benchmark process group is still running")
        time.sleep(0.1)
    return child.wait()

with open(sys.argv[1], "r+") as control:
    fcntl.flock(control, fcntl.LOCK_EX)
    state = read_state(control)
    if state == "cancel":
        write_state(control, "finished")
        sys.exit(130)
    if state != "":
        raise RuntimeError("invalid benchmark control state")
    try:
        child = subprocess.Popen(["sh", "-c", sys.argv[2]], start_new_session=True)
    except Exception:
        write_state(control, "finished")
        raise
    write_state(control, "running")
    fcntl.flock(control, fcntl.LOCK_UN)

    while True:
        fcntl.flock(control, fcntl.LOCK_EX)
        state = read_state(control)
        fcntl.flock(control, fcntl.LOCK_UN)
        if state == "cancel":
            stop_group(child)
            fcntl.flock(control, fcntl.LOCK_EX)
            write_state(control, "finished")
            sys.exit(130)
        status = os.waitid(os.P_PID, child.pid, os.WEXITED | os.WNOHANG | os.WNOWAIT)
        if status is None:
            time.sleep(0.1)
            continue
        fcntl.flock(control, fcntl.LOCK_EX)
        if read_state(control) == "cancel":
            fcntl.flock(control, fcntl.LOCK_UN)
            continue
        fcntl.flock(control, fcntl.LOCK_UN)
        code = stop_group(child)
        fcntl.flock(control, fcntl.LOCK_EX)
        write_state(control, "finished")
        sys.exit(code if code >= 0 else 128 - code)
`

const benchmarkCancelScript = `# neutree-bench-cancel
import fcntl
import sys
import time

with open(sys.argv[1], "r+") as control:
    deadline = time.monotonic() + float(sys.argv[2])
    while True:
        try:
            fcntl.flock(control, fcntl.LOCK_EX | fcntl.LOCK_NB)
        except BlockingIOError:
            if time.monotonic() >= deadline:
                raise TimeoutError("benchmark control lock is unavailable")
            time.sleep(0.1)
            continue
        control.seek(0)
        state = control.read().strip()
        if state == "finished":
            sys.exit(0)
        if state not in ("", "running", "cancel"):
            raise RuntimeError("invalid benchmark control state")
        control.seek(0)
        control.write("cancel\n")
        control.truncate()
        control.flush()
        fcntl.flock(control, fcntl.LOCK_UN)
        if time.monotonic() >= deadline:
            raise TimeoutError("benchmark supervisor did not confirm termination")
        time.sleep(0.1)
`

func benchmarkControlPath(remoteDir string) string {
	return filepath.ToSlash(filepath.Join(remoteDir, benchmarkControlFileName))
}

func prepareRemoteRoot(ctx context.Context, runner remoteRunner, remoteRoot string) error {
	command := "umask 077 && mkdir -- " + shellQuote(remoteRoot)
	if err := runner.Run(ctx, command, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("create exclusive remote benchmark directory: %w", err)
	}

	return nil
}

func prepareBenchmarkControl(ctx context.Context, runner remoteRunner, remoteDir string) error {
	command := "umask 077 && mkdir -- " + shellQuote(remoteDir) +
		" && (set -C; : > " + shellQuote(benchmarkControlPath(remoteDir)) + ")"
	if err := runner.Run(ctx, command, io.Discard, io.Discard); err != nil {
		return fmt.Errorf("prepare remote benchmark control: %w", err)
	}

	return nil
}

func supervisedBenchmarkCommand(remoteDir, command string) string {
	return "python3 -c " + shellQuote(benchmarkSupervisorScript) + " " +
		shellQuote(benchmarkControlPath(remoteDir)) + " " + shellQuote(command)
}

func cancelRemoteBenchmark(ctx context.Context, runner remoteRunner, remoteDir string) error {
	command := "python3 -c " + shellQuote(benchmarkCancelScript) + " " + shellQuote(benchmarkControlPath(remoteDir)) + " 15"
	var stderr bytes.Buffer

	if err := runner.Run(ctx, command, io.Discard, &stderr); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail != "" {
			return fmt.Errorf("cancel remote benchmark: %w: %s", err, detail)
		}

		return fmt.Errorf("cancel remote benchmark: %w", err)
	}

	return nil
}

func runControlledBenchmark(ctx context.Context, runner remoteRunner, remoteDir, command string, stdout, stderr io.Writer) error {
	if err := ctx.Err(); err != nil {
		return err
	}

	runCtx, cancel := context.WithCancel(context.WithoutCancel(ctx))
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- runner.Run(runCtx, supervisedBenchmarkCommand(remoteDir, command), stdout, stderr)
	}()

	var runErr error
	completed := false
	select {
	case runErr = <-done:
		completed = true

		if runErr == nil && ctx.Err() == nil {
			return nil
		}
	case <-ctx.Done():
	}

	if ctx.Err() != nil {
		klog.Infof("benchmark cancellation requested; stopping remote case %s", remoteDir)
	}

	stopCtx, stop := context.WithTimeout(context.Background(), remoteCleanupTimeout)
	stopErr := cancelRemoteBenchmark(stopCtx, runner, remoteDir)

	stop()

	cancel()
	var streamErr error

	if !completed {
		select {
		case runErr = <-done:
		case <-time.After(remoteStreamShutdownTimeout):
			streamErr = fmt.Errorf("remote execution stream did not close")
		}
	}

	if stopErr != nil || streamErr != nil {
		return fmt.Errorf("%w: %w", errRemoteTerminationUnconfirmed, errors.Join(ctx.Err(), runErr, stopErr, streamErr))
	}

	if err := ctx.Err(); err != nil {
		klog.Infof("remote benchmark stopped: %s", remoteDir)
		return err
	}

	return runErr
}
