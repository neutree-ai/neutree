package options

import (
	"github.com/spf13/pflag"
)

type ServerOptions struct {
	Port    int
	Host    string
	GinMode string
}

func NewServerOptions() *ServerOptions {
	return &ServerOptions{
		Port:    3001,
		Host:    "0.0.0.0",
		GinMode: "release",
	}
}

func (o *ServerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.IntVar(&o.Port, "core-server-port", o.Port, "core server port")
	fs.StringVar(&o.Host, "core-server-host", o.Host, "core server host")
	fs.StringVar(&o.GinMode, "gin-mode", o.GinMode, "gin mode: debug, release, test")
}

func (o *ServerOptions) Validate() error {
	return nil
}
