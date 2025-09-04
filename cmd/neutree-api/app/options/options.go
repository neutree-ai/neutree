package options

import (
	"fmt"

	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Options holds all configuration options for the API server
type Options struct {
	Server   *ServerOptions
	Storage  *StorageOptions
	API      *APIOptions
	External *ExternalOptions
}

// NewOptions creates new options with default values
func NewOptions() *Options {
	return &Options{
		Server:   NewServerOptions(),
		Storage:  NewStorageOptions(),
		API:      NewAPIOptions(),
		External: NewExternalOptions(),
	}
}

// AddFlags adds flags for all options structs to the given FlagSet
func (o *Options) AddFlags(fs *pflag.FlagSet) {
	o.Server.AddFlags(fs)
	o.Storage.AddFlags(fs)
	o.API.AddFlags(fs)
	o.External.AddFlags(fs)
}

// Validate validates all options
func (o *Options) Validate() error {
	if err := o.Server.Validate(); err != nil {
		return fmt.Errorf("server options validation failed: %w", err)
	}

	if err := o.Storage.Validate(); err != nil {
		return fmt.Errorf("storage options validation failed: %w", err)
	}

	if err := o.API.Validate(); err != nil {
		return fmt.Errorf("api options validation failed: %w", err)
	}

	if err := o.External.Validate(); err != nil {
		return fmt.Errorf("external options validation failed: %w", err)
	}

	return nil
}

// Config converts options to API configuration
func (o *Options) Config() (*config.APIConfig, error) {
	// Initialize storage
	s, err := storage.New(storage.Options{
		AccessURL: o.Storage.AccessURL,
		Scheme:    "api",
		JwtSecret: o.Storage.JwtSecret,
	})
	if err != nil {
		return nil, fmt.Errorf("failed to init storage: %w", err)
	}

	// Set gin mode
	gin.SetMode(o.API.GinMode)

	// Create gin engine
	engine := gin.Default()

	// Configure JWT authentication
	authConfig := middleware.AuthConfig{
		JwtSecret: o.Storage.JwtSecret,
	}

	// Get external URLs
	grafanaExternalURL, err := util.GetExternalAccessUrl(o.API.DeployType, o.External.GrafanaURL)
	if err != nil {
		return nil, fmt.Errorf("failed to get grafana external url: %w", err)
	}

	return &config.APIConfig{
		Storage:    s,
		GinEngine:  engine,
		AuthConfig: authConfig,

		ServerConfig: &config.ServerConfig{
			Port: o.Server.Port,
			Host: o.Server.Host,
		},

		StaticConfig: &config.StaticConfig{
			Dir: o.API.StaticDir,
		},

		StorageAccessURL: o.Storage.AccessURL,
		AuthEndpoint:     o.External.AuthEndpoint,
		GrafanaURL:       grafanaExternalURL,
		Version:          o.API.Version,
		DeployType:       o.API.DeployType,
	}, nil
}
