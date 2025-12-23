package options

import (
	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"github.com/supabase-community/gotrue-go"
	"k8s.io/klog"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type NeutreeCoreOptions struct {
	Storage       *StorageOptions
	Gateway       *GatewayOptions
	Controller    *ControllerOptions
	Server        *ServerOptions
	Observability *ObservabilityOptions
	Cluster       *ClusterOptions
	Auth          *AuthOptions
}

func NewOptions() *NeutreeCoreOptions {
	return &NeutreeCoreOptions{
		Storage:       NewStorageOptions(),
		Gateway:       NewGatewayOptions(),
		Controller:    NewControllerOptions(),
		Server:        NewServerOptions(),
		Observability: NewObservabilityOptions(),
		Cluster:       NewClusterOptions(),
		Auth:          NewAuthOptions(),
	}
}

func (o *NeutreeCoreOptions) AddFlags(fs *pflag.FlagSet) {
	o.Storage.AddFlags(fs)
	o.Gateway.AddFlags(fs)
	o.Controller.AddFlags(fs)
	o.Server.AddFlags(fs)
	o.Observability.AddFlags(fs)
	o.Cluster.AddFlags(fs)
	o.Auth.AddFlags(fs)
}

func (o *NeutreeCoreOptions) Validate() error {
	if err := o.Auth.Validate(); err != nil {
		return err
	}

	return nil
}

func (o *NeutreeCoreOptions) Config(scheme *scheme.Scheme) (*config.CoreConfig, error) {
	c := &config.CoreConfig{
		Scheme: scheme,
	}

	gin.SetMode(o.Server.GinMode)
	e := gin.Default()
	c.GinEngine = e

	var err error

	// Convert external access URLs for component dependencies
	// Currently, the following configurations need to be converted to external access URLs:
	// 1. Gateway proxy URL, used for user access to inference endpoints
	// 2. Metrics remote write URL, used for remote metric writing; the Neutree cluster may not be on the same Kubernetes cluster as the control plane.
	// Other URLs only require internal access.
	o.Gateway.ProxyUrl, err = util.GetExternalAccessUrl(o.Gateway.ProxyUrl)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to transform gateway proxy url")
	}

	klog.Infof("Transformed gateway proxy url: %s", o.Gateway.ProxyUrl)

	o.Observability.MetricsRemoteWriteURL, err = util.GetExternalAccessUrl(o.Observability.MetricsRemoteWriteURL)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to transform metrics remote write url")
	}

	klog.Infof("Transformed metrics remote write url: %s", o.Observability.MetricsRemoteWriteURL)

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

	objStorage, err := storage.NewObjectStorage(storage.Options{
		AccessURL: o.Storage.AccessURL,
		Scheme:    "api",
		JwtSecret: o.Storage.JwtSecret,
	}, c.Scheme)

	if err != nil {
		return nil, errors.Wrapf(err, "failed to init object storage")
	}

	c.ObjectStorage = objStorage
	c.Storage = s

	imageService := registry.NewImageService()
	c.ImageService = imageService

	gw, err := gateway.GetGateway(o.Gateway.Type, gateway.GatewayOptions{
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

	jwtToken, err := storage.CreateServiceToken(o.Storage.JwtSecret)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to create service token for auth client")
	}

	authClient := gotrue.New("", "").
		WithCustomGoTrueURL(o.Auth.AuthEndpoint).
		WithToken(*jwtToken)

	c.AuthClient = authClient

	return c, nil
}
