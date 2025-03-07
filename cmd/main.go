package main

import (
	"context"
	"flag"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/pkg/postgrest"
)

var (
	postgrestURl      = flag.String("postgrest-url", "http://localhost:6432", "postgrest url")
	controllerWorkers = flag.Int("controller-workers", 5, "controller workers")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	api := postgrest.NewAPI(postgrest.Options{
		AccessURL: *postgrestURl,
		Scheme:    "api",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
		API:     api,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init image registry controller: %s", err.Error())
	}

	klog.Infof("Starting controller")

	go imageRegistryController.Start(ctx)

	<-ctx.Done()
}
