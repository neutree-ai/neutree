package main

import (
	"context"
	"flag"

	"github.com/spf13/pflag"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/neutree-ai/neutree/controllers"
	"github.com/neutree-ai/neutree/internal/cron"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	// todo only support postgrest now.
	storageAccessURL      = flag.String("storage-access-url", "http://postgrest:6432", "postgrest url")
	storageJwtSecret      = flag.String("storage-jwt-secret", "jwt_secret", "storage auth token")
	controllerWorkers     = flag.Int("controller-workers", 5, "controller workers")
	defaultClusterVersion = flag.String("default-cluster-version", "v1", "default neutree cluster version")
	deployType            = flag.String("deploy-type", "local", "deploy type")

	// obs config
	localCollecteConfigPath           = flag.String("local-collect-config-path", "/etc/neutree/collect", "local collect config path")
	kubernetesMetricsCollectConfigMap = flag.String("kubernetes-metrics-collect-configmap", "vmagent-scrape-config", "kubernetes collect config name")
	kubernetesCollectConfigNamespace  = flag.String("kubernetes-collect-config-namespace", "neutree", "kubernetes collect config namespace")
	metricsRemoteWriteURL             = flag.String("metrics-remote-write-url", "", "metrics remote write url")

	// gateway config
	gatewayType              = flag.String("gateway-type", "none", "gateway type")
	gatewayProxyUrl          = flag.String("gateway-proxy-url", "", "gateway proxy url")
	gatewayAdminUrl          = flag.String("gateway-admin-url", "", "gateway admin url")
	gatewayLogRemoteWriteUrl = flag.String("gateway-log-remote-write-url", "", "log remote write url")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()
	ctrl.SetLogger(klog.NewKlogr())

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	obsCollectConfigManager, err := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		DeployType:                            *deployType,
		LocalCollectConfigPath:                *localCollecteConfigPath,
		KubernetesMetricsCollectConfigMapName: *kubernetesMetricsCollectConfigMap,
		KubernetesCollectConfigNamespace:      *kubernetesCollectConfigNamespace,
	})
	if err != nil {
		klog.Fatalf("failed to init obs collect config manager: %s", err.Error())
	}

	s, err := storage.New(storage.Options{
		AccessURL: *storageAccessURL,
		Scheme:    "api",
		JwtSecret: *storageJwtSecret,
	})
	if err != nil {
		klog.Fatalf("failed to init storage: %s", err.Error())
	}

	imageService := registry.NewImageService()

	gw, err := gateway.GetGateway(*gatewayType, gateway.GatewayOptions{
		DeployType:        *deployType,
		ProxyUrl:          *gatewayProxyUrl,
		AdminUrl:          *gatewayAdminUrl,
		LogRemoteWriteUrl: *gatewayLogRemoteWriteUrl,
		Storage:           s,
	})
	if err != nil {
		klog.Fatalf("failed to init gateway: %s", err.Error())
	}

	err = gw.Init()
	if err != nil {
		klog.Fatalf("failed to init gateway: %s", err.Error())
	}

	imageRegistryController, err := controllers.NewImageRegistryController(&controllers.ImageRegistryControllerOption{
		Storage:      s,
		Workers:      *controllerWorkers,
		ImageService: imageService,
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

	clusterController, err := controllers.NewClusterController(&controllers.ClusterControllerOption{
		Storage:                 s,
		Workers:                 *controllerWorkers,
		DefaultClusterVersion:   *defaultClusterVersion,
		ImageService:            imageService,
		ObsCollectConfigManager: obsCollectConfigManager,
		MetricsRemoteWriteURL:   *metricsRemoteWriteURL,
		Gw:                      gw,
	})

	if err != nil {
		klog.Fatalf("failed to init cluster controller: %s", err.Error())
	}

	engineController, err := controllers.NewEngineController(&controllers.EngineControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init engine controller: %s", err.Error())
	}

	endpointController, err := controllers.NewEndpointController(&controllers.EndpointControllerOption{
		Storage:      s,
		Workers:      *controllerWorkers,
		ImageService: imageService,
		Gw:           gw,
	})
	if err != nil {
		klog.Fatalf("failed to init endpoint controller: %s", err.Error())
	}

	roleController, err := controllers.NewRoleController(&controllers.RoleControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init role controller: %s", err.Error())
	}

	roleAssignmentController, err := controllers.NewRoleAssignmentController(&controllers.RoleAssignmentControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init role assignment controller: %s", err.Error())
	}

	workspaceController, err := controllers.NewWorkspaceController(&controllers.WorkspaceControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init workspace controller: %s", err.Error())
	}

	apiKeyController, err := controllers.NewApiKeyController(&controllers.ApiKeyControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
		Gw:      gw,
	})
	if err != nil {
		klog.Fatalf("failed to init api key controller: %s", err.Error())
	}

	modelCatalogController, err := controllers.NewModelCatalogController(&controllers.ModelCatalogControllerOption{
		Storage: s,
		Workers: *controllerWorkers,
	})
	if err != nil {
		klog.Fatalf("failed to init model catalog controller: %s", err.Error())
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	if err = cron.StartCrons(ctx, s); err != nil {
		klog.Fatalf("failed to start crons: %s", err.Error())
	}

	go imageRegistryController.Start(ctx)
	go modelRegistryController.Start(ctx)
	go clusterController.Start(ctx)
	go engineController.Start(ctx)
	go endpointController.Start(ctx)
	go roleController.Start(ctx)
	go roleAssignmentController.Start(ctx)
	go workspaceController.Start(ctx)
	go apiKeyController.Start(ctx)
	go modelCatalogController.Start(ctx)

	go obsCollectConfigManager.Start(ctx)

	<-ctx.Done()
}
