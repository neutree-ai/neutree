package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	nodeagentapp "github.com/neutree-ai/neutree/cmd/neutree-node-agent/app"
	"github.com/neutree-ai/neutree/internal/version"
)

func main() {
	info := version.Get()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)

	application, err := nodeagentapp.NewBuilder().
		WithArgs(os.Args[1:]).
		WithBuildInfo(nodeagentapp.BuildInfo{
			Version:   info.AppVersion,
			GitCommit: info.GitCommit,
			BuildTime: info.BuildTime,
		}).
		Build()
	if err == nil {
		err = application.Run(ctx)
	}

	cancel()

	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
