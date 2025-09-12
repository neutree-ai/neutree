package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"syscall"

	"k8s.io/klog/v2"

	"github.com/spf13/pflag"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/options"
	"github.com/neutree-ai/neutree/pkg/scheme"
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

	scheme := scheme.NewScheme()
	v1.AddToScheme(scheme) //nolint:errcheck

	c, err := opts.Config(scheme)
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
