package config

import (
	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
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
	// Core dependencies
	Storage    storage.Storage
	GinEngine  *gin.Engine
	AuthConfig middleware.AuthConfig

	// Server configuration
	ServerConfig *ServerConfig
	StaticConfig *StaticConfig

	// External services
	StorageAccessURL string
	AuthEndpoint     string
	GrafanaURL       string
	Version          string
	DeployType       string
}
