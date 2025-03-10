package main

import (
	"context"
	"flag"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	// todo only support postgrest now.
	storageAccessURL  = flag.String("storage-access-url", "http://postgrest:6432", "postgrest url")
	controllerWorkers = flag.Int("controller-workers", 5, "controller workers")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	s := storage.New(storage.Options{
		AccessURL: *storageAccessURL,
		Scheme:    "api",
	})

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init image registry controller: %s", err.Error())
	}

	modelRegistryController, err := controllers.NewModelRegistryController(&controllers.ModelRegistryControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})

	if err != nil {
		klog.Fatalf("failed to init model registry controller: %s", err.Error())
	}

	klog.Infof("Starting controller")

	go imageRegistryController.Start(ctx)
	go modelRegistryController.Start(ctx)

	<-ctx.Done()
}
