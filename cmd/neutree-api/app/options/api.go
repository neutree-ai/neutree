package options

import (
	"time"

	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/internal/model_registry"
)

// APIOptions holds API application configuration options
type APIOptions struct {
	GinMode   string
	StaticDir string
	Version   string
	// PublicRegistryQueryCacheTTL is how long a public model registry's answer to
	// a search or a page is reused before the hub is asked again. Zero or less
	// falls back to the package default.
	PublicRegistryQueryCacheTTL time.Duration
}

// NewAPIOptions creates new API options with default values
func NewAPIOptions() *APIOptions {
	return &APIOptions{
		GinMode:                     "release",
		StaticDir:                   "./public",
		PublicRegistryQueryCacheTTL: model_registry.DefaultQueryCacheTTL,
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *APIOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.GinMode, "gin-mode", o.GinMode, "gin mode: debug, release, test")
	fs.StringVar(&o.StaticDir, "static-dir", o.StaticDir, "directory for static files")
	fs.DurationVar(&o.PublicRegistryQueryCacheTTL, "public-registry-query-cache-ttl", o.PublicRegistryQueryCacheTTL,
		"how long a public model registry's query results are reused before the hub is asked again")
}

// Validate validates API options
func (o *APIOptions) Validate() error {
	return nil
}
