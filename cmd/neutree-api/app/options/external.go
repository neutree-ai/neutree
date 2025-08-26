package options

import (
	"github.com/spf13/pflag"
)

// ExternalOptions holds external service configuration options
type ExternalOptions struct {
	AuthEndpoint string
	GrafanaURL   string
}

// NewExternalOptions creates new external options with default values
func NewExternalOptions() *ExternalOptions {
	return &ExternalOptions{
		AuthEndpoint: "http://auth:9999",
		GrafanaURL:   "",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *ExternalOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AuthEndpoint, "auth-endpoint", o.AuthEndpoint, "auth service endpoint")
	fs.StringVar(&o.GrafanaURL, "grafana-url", o.GrafanaURL, "grafana url for system info API")
}

// Validate validates external options
func (o *ExternalOptions) Validate() error {
	return nil
}
