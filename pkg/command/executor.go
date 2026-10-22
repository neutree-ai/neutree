package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	stderrLimit      = 16 * 1024
	stderrTruncated  = "[stderr truncated]\n"
	processWaitDelay = time.Second
)

type Executor interface {
	Execute(ctx context.Context, name string, args []string) ([]byte, error)
	ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args []string) ([]byte, error)
}

type OSExecutor struct{}

func (e *OSExecutor) Execute(ctx context.Context, name string, args []string) ([]byte, error) {
	var stdout bytes.Buffer
	var stderr stderrBuffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cmd.WaitDelay = processWaitDelay

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}

		if diagnostic := strings.TrimSpace(stderr.String()); diagnostic != "" {
			err = fmt.Errorf("%w: %s", err, diagnostic)
		}
	}

	return stdout.Bytes(), err
}

func (e *OSExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return e.Execute(ctx, name, args)
}

type stderrBuffer struct {
	buffer    []byte
	truncated bool
}

func (writer *stderrBuffer) Write(data []byte) (int, error) {
	size := len(data)

	if size > stderrLimit {
		writer.truncated = true
		data = data[size-stderrLimit:]
	}

	if overflow := len(writer.buffer) + len(data) - stderrLimit; overflow > 0 {
		writer.truncated = true
		writer.buffer = writer.buffer[:copy(writer.buffer, writer.buffer[overflow:])]
	}

	writer.buffer = append(writer.buffer, data...)

	return size, nil
}

func (writer *stderrBuffer) String() string {
	if writer.truncated {
		return stderrTruncated + string(writer.buffer[len(stderrTruncated):])
	}

	return string(writer.buffer)
}
