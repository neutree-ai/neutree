package options

import (
	"os"

	"github.com/spf13/pflag"
)

type GatewayOptions struct {
	InternalToken     string
	Type              string
	ProxyUrl          string
	AdminUrl          string
	LogRemoteWriteUrl string
}

func NewGatewayOptions() *GatewayOptions {
	return &GatewayOptions{
		InternalToken:     os.Getenv("NEUTREE_GATEWAY_INTERNAL_TOKEN"),
		Type:              "none",
		ProxyUrl:          "",
		AdminUrl:          "",
		LogRemoteWriteUrl: "",
	}
}

func (o *GatewayOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.InternalToken, "gateway-internal-token", o.InternalToken, "shared token for the inference front proxy (empty disables trust)")

	fs.Lookup("gateway-internal-token").DefValue = "" // Do not expose the environment secret in --help.
	fs.StringVar(&o.Type, "gateway-type", o.Type, "gateway type")
	fs.StringVar(&o.ProxyUrl, "gateway-proxy-url", o.ProxyUrl, "gateway proxy url")
	fs.StringVar(&o.AdminUrl, "gateway-admin-url", o.AdminUrl, "gateway admin url")
	fs.StringVar(&o.LogRemoteWriteUrl, "gateway-log-remote-write-url", o.LogRemoteWriteUrl, "log remote write url")
}

func (o *GatewayOptions) Validate() error {
	return nil
}
