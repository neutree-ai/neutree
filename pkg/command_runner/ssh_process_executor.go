package command_runner

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
	"time"
)

const (
	sshStderrLimit      = 16 * 1024
	sshStderrTruncated  = "[stderr truncated]\n"
	sshProcessWaitDelay = time.Second
)

type SSHProcessOutput struct {
	Stdout string
	Stderr string
}

type SSHProcessExecute func(context.Context, string, []string) (SSHProcessOutput, error)

func executeSSHProcess(ctx context.Context, name string, args []string) (SSHProcessOutput, error) {
	var stdout bytes.Buffer
	var stderr sshStderrBuffer
	command := exec.CommandContext(ctx, name, args...)
	command.Stdout = &stdout
	command.Stderr = &stderr
	command.WaitDelay = sshProcessWaitDelay

	err := command.Run()
	if err != nil && ctx.Err() != nil {
		err = errors.Join(err, ctx.Err())
	}

	return SSHProcessOutput{Stdout: stdout.String(), Stderr: stderr.String()}, err
}

type sshStderrBuffer struct {
	buffer    []byte
	truncated bool
}

func (writer *sshStderrBuffer) Write(data []byte) (int, error) {
	size := len(data)

	if size > sshStderrLimit {
		writer.truncated = true
		data = data[size-sshStderrLimit:]
	}

	if overflow := len(writer.buffer) + len(data) - sshStderrLimit; overflow > 0 {
		writer.truncated = true
		writer.buffer = writer.buffer[:copy(writer.buffer, writer.buffer[overflow:])]
	}

	writer.buffer = append(writer.buffer, data...)

	return size, nil
}

func (writer *sshStderrBuffer) String() string {
	if writer.truncated {
		return sshStderrTruncated + string(writer.buffer[len(sshStderrTruncated):])
	}

	return string(writer.buffer)
}
