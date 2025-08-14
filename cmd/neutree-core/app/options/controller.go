package options

import (
	"github.com/spf13/pflag"
)

type ControllerOptions struct {
	Workers int
}

func NewControllerOptions() *ControllerOptions {
	return &ControllerOptions{
		Workers: 5,
	}
}

func (o *ControllerOptions) AddFlags(fs *pflag.FlagSet) {
	fs.IntVar(&o.Workers, "controller-workers", o.Workers, "controller workers")
}

func (o *ControllerOptions) Validate() error {
	return nil
}
