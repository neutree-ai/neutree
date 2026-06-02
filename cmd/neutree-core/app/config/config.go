package config

import (
	"github.com/gin-gonic/gin"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/engine"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/scheme"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type ControllerConfig struct {
	Workers int
}

type ClusterControllerConfig struct {
	DefaultClusterVersion string
	MetricsRemoteWriteURL string
}

type ServerConfig struct {
	Port int
	Host string
}

type CoreConfig struct {
	ObjectStorage           storage.ObjectStorage
	Storage                 storage.Storage
	PortAllocationStorage   storage.PortAllocationStorage
	ImageService            registry.ImageService
	Gateway                 gateway.Gateway
	AcceleratorManager      accelerator.Manager
	EngineRegistry          engine.Registry
	ObsCollectConfigManager manager.ObsCollectConfigManager
	GinEngine               *gin.Engine
	AuthClient              auth.Client

	// global controller configs
	ControllerConfig *ControllerConfig

	// cluster controller specific config
	ClusterControllerConfig *ClusterControllerConfig

	// core server config
	ServerConfig *ServerConfig

	// global port allocator range for SSH / K8s side-channel and bootstrap ports
	PortRange *v1.PortRangeSpec

	Scheme *scheme.Scheme
}
