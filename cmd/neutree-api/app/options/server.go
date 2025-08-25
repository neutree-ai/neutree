package options

import (
	"github.com/spf13/pflag"
)

// ServerOptions holds server configuration options
type ServerOptions struct {
	Port int
	Host string
}

// NewServerOptions creates new server options with default values
func NewServerOptions() *ServerOptions {
	return &ServerOptions{
		Port: 3000,
		Host: "0.0.0.0",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.IntVar(&o.Port, "port", o.Port, "API server port")
	fs.StringVar(&o.Host, "host", o.Host, "API server host")
}

// Validate validates server options
func (o *ServerOptions) Validate() error {
	return nil
}
