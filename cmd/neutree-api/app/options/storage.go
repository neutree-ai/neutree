package options

import (
	"github.com/spf13/pflag"
)

// StorageOptions holds storage configuration options
type StorageOptions struct {
	AccessURL string
	JwtSecret string
}

// NewStorageOptions creates new storage options with default values
func NewStorageOptions() *StorageOptions {
	return &StorageOptions{
		AccessURL: "http://postgrest:6432",
		JwtSecret: "",
	}
}

// AddFlags adds flags for this options struct to the given FlagSet
func (o *StorageOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AccessURL, "storage-access-url", o.AccessURL, "postgrest url")
	fs.StringVar(&o.JwtSecret, "storage-jwt-secret", o.JwtSecret, "storage auth token (JWT_SECRET)")
}

// Validate validates storage options
func (o *StorageOptions) Validate() error {
	return nil
}
