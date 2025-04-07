package main

import (
	"context"
	"flag"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/orchestrator"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	// todo only support postgrest now.
	storageAccessURL      = flag.String("storage-access-url", "http://postgrest:6432", "postgrest url")
	storageJwtSecret      = flag.String("storage-jwt-secret", "jwt_secret", "storage auth token")
	controllerWorkers     = flag.Int("controller-workers", 5, "controller workers")
	defaultClusterVersion = flag.String("default-cluster-version", "v1", "default neutree cluster version")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	s, err := storage.New(storage.Options{
		AccessURL: *storageAccessURL,
		Scheme:    "api",
		JwtSecret: *storageJwtSecret,
	})
	if err != nil {
		klog.Fatalf("failed to init storage: %s", err.Error())
	}

	imageService := registry.NewImageService()

	imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
		Storage:      s,
		Workers:      *controllerWorkers,
		ImageService: imageService,
	})
	if err != nil {
		klog.Fatalf("failed to init image registry controller: %s", err.Error())
	}

	modelRegistryController, err := controllers.NewModelRegistryController(&controllers.ModelRegistryControllerOption{
		Storage:          s,
		Workers:          *controllerWorkers,
		NewModelRegistry: model_registry.New,
	})

	if err != nil {
		klog.Fatalf("failed to init model registry controller: %s", err.Error())
	}

	clusterController, err := controllers.NewClusterController(&controllers.ClusterControllerOption{
		Storage:              s,
		Workers:              *controllerWorkers,
		DefaultClusterVesion: *defaultClusterVersion,
		NewOrchestrator:      orchestrator.NewOrchestrator,
	})

	if err != nil {
		klog.Fatalf("failed to init cluster controller: %s", err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go imageRegistryController.Start(ctx)
	go modelRegistryController.Start(ctx)
	go clusterController.Start(ctx)

	<-ctx.Done()
}
