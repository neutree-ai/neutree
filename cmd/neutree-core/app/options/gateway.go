package options

import (
	"github.com/spf13/pflag"
)

type GatewayOptions struct {
	Type              string
	ProxyUrl          string
	AdminUrl          string
	LogRemoteWriteUrl string
	NeutreeApiUrl     string
}

func NewGatewayOptions() *GatewayOptions {
	return &GatewayOptions{
		Type:              "none",
		ProxyUrl:          "",
		AdminUrl:          "",
		LogRemoteWriteUrl: "",
		NeutreeApiUrl:     "http://neutree-api:3000",
	}
}

func (o *GatewayOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Type, "gateway-type", o.Type, "gateway type")
	fs.StringVar(&o.ProxyUrl, "gateway-proxy-url", o.ProxyUrl, "gateway proxy url")
	fs.StringVar(&o.AdminUrl, "gateway-admin-url", o.AdminUrl, "gateway admin url")
	fs.StringVar(&o.LogRemoteWriteUrl, "gateway-log-remote-write-url", o.LogRemoteWriteUrl, "log remote write url")
	fs.StringVar(&o.NeutreeApiUrl, "gateway-neutree-api-url", o.NeutreeApiUrl, "neutree-api base url for gateway quota lookups")
}

func (o *GatewayOptions) Validate() error {
	return nil
}
