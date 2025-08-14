package config

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/internal/registry"
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
	Storage                 storage.Storage
	ImageService            registry.ImageService
	Gateway                 gateway.Gateway
	AcceleratorManager      accelerator.Manager
	ObsCollectConfigManager manager.ObsCollectConfigManager
	GinEngine               *gin.Engine

	// global controller configs
	ControllerConfig *ControllerConfig

	// cluster controller specific config
	ClusterControllerConfig *ClusterControllerConfig

	// core server config
	ServerConfig *ServerConfig
}
