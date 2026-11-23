package command

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"time"
)

type Executor interface {
	Execute(ctx context.Context, name string, args []string) ([]byte, error)
	ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args []string) ([]byte, error)
}

type OSExecutor struct{}

func (e *OSExecutor) Execute(ctx context.Context, name string, args []string) ([]byte, error) {
	var stdout, stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, name, args...)
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()
	if err != nil {
		if ctx.Err() != nil {
			err = errors.Join(err, ctx.Err())
		}

		if stderr.Len() > 0 {
			err = fmt.Errorf("%w: %s", err, stderr.String())
		}
	}

	return stdout.Bytes(), err
}

func (e *OSExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args []string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return e.Execute(ctx, name, args)
}
