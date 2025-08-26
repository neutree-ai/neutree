package options

import "github.com/spf13/pflag"

type ClusterOptions struct {
	DefaultClusterVersion string
}

func NewClusterOptions() *ClusterOptions {
	return &ClusterOptions{
		DefaultClusterVersion: "v1",
	}
}

func (o *ClusterOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.DefaultClusterVersion, "default-cluster-version", o.DefaultClusterVersion, "default neutree cluster version")
}
