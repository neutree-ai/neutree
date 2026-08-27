package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	nodeagentapp "github.com/neutree-ai/neutree/cmd/neutree-node-agent/app"
	"github.com/neutree-ai/neutree/internal/version"
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "version" || os.Args[1] == "--version") {
		fmt.Println(version.Get().String())
		return
	}

	klog.InitFlags(nil)
	defer klog.Flush()

	if err := flag.Set("v", "2"); err != nil {
		klog.Fatalf("Set default log verbosity: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		signalContext := make(chan os.Signal, 1)
		signal.Notify(signalContext, syscall.SIGINT, syscall.SIGTERM)
		defer signal.Stop(signalContext)

		<-signalContext
		klog.Info("Received shutdown signal")
		cancel()
	}()

	application, err := nodeagentapp.NewBuilder().
		WithAdapters(nodeagentapp.DefaultAdapters()...).
		Build()
	if err != nil {
		klog.Fatalf("Build application: %v", err)
	}

	application.AddFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	if err := application.Run(ctx); err != nil {
		klog.Fatalf("Application failed: %v", err)
	}
}
