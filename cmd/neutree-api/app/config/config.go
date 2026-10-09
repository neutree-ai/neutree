package config

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/clustercache"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// ServerConfig holds server configuration
type ServerConfig struct {
	Port int
	Host string
}

// StaticConfig holds static file serving configuration
type StaticConfig struct {
	Dir string
}

// APIConfig holds the main API configuration
type APIConfig struct {
	EndpointCacheProvider clustercache.EndpointProvider
	// ClusterCacheSupported is set only by distributions that install a cache provider.
	ClusterCacheSupported bool
	// Core dependencies
	Storage    storage.Storage
	GinEngine  *gin.Engine
	AuthConfig middleware.AuthConfig

	// Server configuration
	ServerConfig *ServerConfig
	StaticConfig *StaticConfig

	// PublicRegistryQueryCacheTTL is how long a public model registry's query
	// results are reused. Zero uses the model registry package's default.
	PublicRegistryQueryCacheTTL time.Duration

	// External services
	StorageAccessURL string
	AuthEndpoint     string
	GrafanaURL       string
	AITraceStoreURL  string
	Version          string
}
