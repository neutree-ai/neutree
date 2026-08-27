package config

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/accelerator"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/engine"
	"github.com/neutree-ai/neutree/internal/gateway"
	"github.com/neutree-ai/neutree/internal/model_registry"
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

// ModelRegistryConfig is what this deployment provisions and offers in the way
// of model registries.
type ModelRegistryConfig struct {
	Builtin model_registry.BuiltinConfig
}

type ServerConfig struct {
	Port int
	Host string
}

type CoreConfig struct {
	ObjectStorage           storage.ObjectStorage
	Storage                 storage.Storage
	ImageService            registry.ImageService
	RepositoryService       registry.RepositoryService
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

	// model registry provisioning config
	ModelRegistryConfig *ModelRegistryConfig

	Scheme *scheme.Scheme
}
