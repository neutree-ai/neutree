package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/neutree-ai/neutree/internal/version"
	"github.com/neutree-ai/neutree/pkg/nodeagent"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	info := version.Get()

	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	return nodeagent.Run(ctx, nodeagent.Config{
		Args: os.Args[1:],
		Build: nodeagent.BuildInfo{
			Version:   info.AppVersion,
			GitCommit: info.GitCommit,
			BuildTime: info.BuildTime,
		},
		Adapters: nodeagent.DefaultAdapters(),
	})
}
