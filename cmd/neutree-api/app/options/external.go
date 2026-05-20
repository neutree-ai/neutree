package options

import (
	"github.com/spf13/pflag"
)

// ExternalOptions holds external service configuration options
type ExternalOptions struct {
	AuthEndpoint    string
	GrafanaURL      string
	AITraceStoreURL string
}

// NewExternalOptions creates new external options with default values
func NewExternalOptions() *ExternalOptions {
	return &ExternalOptions{
		AuthEndpoint:    "http://auth:9999",
		GrafanaURL:      "",
		AITraceStoreURL: "",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *ExternalOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AuthEndpoint, "auth-endpoint", o.AuthEndpoint, "auth service endpoint")
	fs.StringVar(&o.GrafanaURL, "grafana-url", o.GrafanaURL, "grafana url for system info API")
	fs.StringVar(&o.AITraceStoreURL, "ai-trace-store-url", o.AITraceStoreURL, "VictoriaLogs base URL for AI inference trace queries (e.g. http://victorialogs:9428)")
}

// Validate validates external options
func (o *ExternalOptions) Validate() error {
	return nil
}
