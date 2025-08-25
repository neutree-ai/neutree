package options

import "github.com/spf13/pflag"

// DeployOptions contains deployment-related configuration
type DeployOptions struct {
	Type string
}

// NewDeployOptions creates default deploy options
func NewDeployOptions() *DeployOptions {
	return &DeployOptions{
		Type: "local",
	}
}

func (o *DeployOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.Type, "deploy-type", o.Type, "deployment type (e.g., local, kubernetes)")
}
