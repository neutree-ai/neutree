package options

import (
	"github.com/spf13/pflag"
)

// APIOptions holds API application configuration options
type APIOptions struct {
	GinMode   string
	StaticDir string
	Version   string
}

// NewAPIOptions creates new API options with default values
func NewAPIOptions() *APIOptions {
	return &APIOptions{
		GinMode:   "release",
		StaticDir: "./public",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *APIOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.GinMode, "gin-mode", o.GinMode, "gin mode: debug, release, test")
	fs.StringVar(&o.StaticDir, "static-dir", o.StaticDir, "directory for static files")
}

// Validate validates API options
func (o *APIOptions) Validate() error {
	return nil
}
