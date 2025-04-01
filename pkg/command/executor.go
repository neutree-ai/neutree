package command

import (
	"context"
	"os/exec"
	"time"
)

type Executor interface {
	Execute(ctx context.Context, name string, args ...string) ([]byte, error)
	ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error)
}

type OSExecutor struct{}

func (e *OSExecutor) Execute(ctx context.Context, name string, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, name, args...)
	return cmd.CombinedOutput()
}

func (e *OSExecutor) ExecuteWithTimeout(ctx context.Context, timeout time.Duration, name string, args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	return e.Execute(ctx, name, args...)
}
