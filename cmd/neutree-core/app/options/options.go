package options

import (
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type NeutreeCoreOptions struct {
	Storage       *StorageOptions
	Gateway       *GatewayOptions
	Deploy        *DeployOptions
	Controller    *ControllerOptions
	Server        *ServerOptions
	Observability *ObservabilityOptions
	Cluster       *ClusterOptions
}

func NewOptions() *NeutreeCoreOptions {
	return &NeutreeCoreOptions{
		Storage:       NewStorageOptions(),
		Gateway:       NewGatewayOptions(),
		Deploy:        NewDeployOptions(),
		Controller:    NewControllerOptions(),
		Server:        NewServerOptions(),
		Observability: NewObservabilityOptions(),
		Cluster:       NewClusterOptions(),
	}
}

func (o *NeutreeCoreOptions) AddFlags(fs *pflag.FlagSet) {
	o.Storage.AddFlags(fs)
	o.Gateway.AddFlags(fs)
	o.Deploy.AddFlags(fs)
	o.Controller.AddFlags(fs)
	o.Server.AddFlags(fs)
	o.Observability.AddFlags(fs)
	o.Cluster.AddFlags(fs)
}

func (o *NeutreeCoreOptions) Validate() error {
	return nil
}

func (o *NeutreeCoreOptions) Config() (*config.CoreConfig, error) {
	c := &config.CoreConfig{}

	gin.SetMode(o.Server.GinMode)
	e := gin.Default()
	c.GinEngine = e

	acceleratorManager := accelerator.NewManager(e)
	c.AcceleratorManager = acceleratorManager

	s, err := storage.New(storage.Options{
		AccessURL: o.Storage.AccessURL,
		Scheme:    "api",
		JwtSecret: o.Storage.JwtSecret,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to init storage")
	}

	c.Storage = s

	imageService := registry.NewImageService()
	c.ImageService = imageService

	gw, err := gateway.GetGateway(o.Gateway.Type, gateway.GatewayOptions{
		DeployType:        o.Deploy.Type,
		ProxyUrl:          o.Gateway.ProxyUrl,
		AdminUrl:          o.Gateway.AdminUrl,
		LogRemoteWriteUrl: o.Gateway.LogRemoteWriteUrl,
		Storage:           s,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to init gateway")
	}

	err = gw.Init()
	if err != nil {
		return nil, errors.Wrapf(err, "failed to init gateway")
	}

	c.Gateway = gw

	obsCollectConfigManager, err := manager.NewObsCollectConfigManager(manager.ObsCollectConfigOptions{
		DeployType:                            o.Deploy.Type,
		LocalCollectConfigPath:                o.Observability.LocalCollectConfigPath,
		KubernetesMetricsCollectConfigMapName: o.Observability.KubernetesMetricsCollectConfigMap,
		KubernetesCollectConfigNamespace:      o.Observability.KubernetesCollectConfigNamespace,
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to init obs collect config manager")
	}

	c.ObsCollectConfigManager = obsCollectConfigManager

	c.ControllerConfig = &config.ControllerConfig{
		Workers: o.Controller.Workers,
	}
	c.ClusterControllerConfig = &config.ClusterControllerConfig{
		DefaultClusterVersion: o.Cluster.DefaultClusterVersion,
		MetricsRemoteWriteURL: o.Observability.MetricsRemoteWriteURL,
	}
	c.ServerConfig = &config.ServerConfig{
		Port: o.Server.Port,
		Host: o.Server.Host,
	}

	return c, nil
}
