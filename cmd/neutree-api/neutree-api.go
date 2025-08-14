package main

import (
	"context"
	"flag"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app"
	"github.com/neutree-ai/neutree/cmd/neutree-api/app/options"
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	// Initialize options
	opts := options.NewOptions()

	// Add flags from options
	opts.AddFlags(pflag.CommandLine)

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	// Validate options
	if err := opts.Validate(); err != nil {
		klog.Fatalf("Options validation failed: %s", err.Error())
	}

	// Convert options to configuration
	config, err := opts.Config()
	if err != nil {
		klog.Fatalf("Failed to create configuration: %s", err.Error())
	}

	// Create and build application
	builder := app.NewBuilder().WithConfig(config)

	application, err := builder.Build()
	if err != nil {
		klog.Fatalf("Failed to build application: %s", err.Error())
	}

	// Run application
	// Create context with cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err := application.Run(ctx); err != nil {
		klog.Fatalf("Failed to run application: %s", err.Error())
	}
}
