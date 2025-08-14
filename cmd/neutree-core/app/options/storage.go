package options

import (
	"github.com/spf13/pflag"
)

type StorageOptions struct {
	AccessURL string
	JwtSecret string
}

func NewStorageOptions() *StorageOptions {
	return &StorageOptions{
		AccessURL: "http://postgrest:6432",
		JwtSecret: "jwt_secret",
	}
}

func (o *StorageOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AccessURL, "storage-access-url", o.AccessURL, "postgrest url")
	fs.StringVar(&o.JwtSecret, "storage-jwt-secret", o.JwtSecret, "storage auth token")
}

func (o *StorageOptions) Validate() error {
	return nil
}
