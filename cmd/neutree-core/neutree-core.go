package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/options"
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Handle signals
	go func() {
		sigCh := make(chan os.Signal, 1)
		signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM)
		<-sigCh
		klog.Info("Received shutdown signal")
		cancel()
	}()

	opts := options.NewOptions()
	opts.AddFlags(pflag.CommandLine)
	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	err := opts.Validate()
	if err != nil {
		klog.Fatalf("Invalid options: %v", err)
	}

	c, err := opts.Config()
	if err != nil {
		klog.Fatalf("Failed to create config: %v", err)
	}

	// Build application
	app, err := app.NewBuilder().
		WithConfig(c).
		Build()

	if err != nil {
		klog.Fatalf("Failed to build application: %v", err)
	}

	// Run application
	if err := app.Run(ctx); err != nil {
		klog.Fatalf("Application failed: %v", err)
	}
}
