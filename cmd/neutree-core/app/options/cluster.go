package options

import (
	"github.com/spf13/pflag"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/portalloc"
)

type ClusterOptions struct {
	DefaultClusterVersion string
	PortRangeStart        int
	PortRangeEnd          int
}

func NewClusterOptions() *ClusterOptions {
	return &ClusterOptions{
		DefaultClusterVersion: "v1",
		PortRangeStart:        v1.DefaultPortRange.Start,
		PortRangeEnd:          v1.DefaultPortRange.End,
	}
}

func (o *ClusterOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.DefaultClusterVersion, "default-cluster-version", o.DefaultClusterVersion, "default neutree cluster version")
	fs.IntVar(&o.PortRangeStart, "port-range-start", o.PortRangeStart, "global start port for endpoint side-channel/bootstrap allocation")
	fs.IntVar(&o.PortRangeEnd, "port-range-end", o.PortRangeEnd, "global end port for endpoint side-channel/bootstrap allocation")
}

func (o *ClusterOptions) PortRange() v1.PortRangeSpec {
	return v1.PortRangeSpec{Start: o.PortRangeStart, End: o.PortRangeEnd}
}

func (o *ClusterOptions) Validate() error {
	return portalloc.ValidatePortRange(o.PortRange())
}
