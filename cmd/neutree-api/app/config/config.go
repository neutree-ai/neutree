package config

import (
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
	"github.com/neutree-ai/neutree/pkg/identity/oidc"
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

	// PublicRegistryQueryCacheTTL is how long a public model registry's query
	// results are reused. Zero uses the model registry package's default.
	PublicRegistryQueryCacheTTL time.Duration

	// External services
	StorageAccessURL string
	AuthEndpoint     string
	GrafanaURL       string
	AITraceStoreURL  string
	Version          string

	// LDAP is the directory users log in with; nil disables LDAP login.
	LDAP *ldap.Config
	// OIDC is the OpenID Connect provider users log in with; nil disables OIDC
	// login.
	OIDC *OIDCConfig
}

// OIDCConfig configures OIDC login.
type OIDCConfig struct {
	// ID names the provider in user links and in new users' placeholder emails;
	// keep it stable once users have logged in, or they are linked anew.
	ID       string
	Provider oidc.Config
	// AllowedRedirects are the UI URLs a login may return to.
	AllowedRedirects []string
}
