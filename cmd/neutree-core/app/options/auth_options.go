package options

import (
	"fmt"

	"github.com/spf13/pflag"
)

type AuthOptions struct {
	AuthEndpoint string
}

func NewAuthOptions() *AuthOptions {
	return &AuthOptions{
		AuthEndpoint: "",
	}
}

func (o *AuthOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.AuthEndpoint, "auth-endpoint", o.AuthEndpoint, "GoTrue authentication service endpoint")
}

func (o *AuthOptions) Validate() error {
	if o.AuthEndpoint == "" {
		return fmt.Errorf("--auth-endpoint is required")
	}

	return nil
}
