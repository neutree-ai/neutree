package options

import (
	"github.com/spf13/pflag"

	"github.com/neutree-ai/neutree/internal/model_registry"
)

// ModelRegistryOptions configures the model registries the control plane
// provisions for itself.
type ModelRegistryOptions struct {
	// EnableBuiltinPublicRegistries provisions a read-only registry for each
	// supported public hub into every workspace.
	//
	// Off by default, and deliberately so: an installation with no route out to
	// the internet that shipped one enabled would show every user a registry
	// permanently stuck in Failed.
	EnableBuiltinPublicRegistries bool
	// HuggingFaceEndpoint is the address the built-in Hugging Face registry
	// points at — the Hub, or a mirror reachable from inside the network. It has
	// no effect unless the built-in registries are enabled.
	HuggingFaceEndpoint string
}

func NewModelRegistryOptions() *ModelRegistryOptions {
	return &ModelRegistryOptions{
		EnableBuiltinPublicRegistries: false,
		HuggingFaceEndpoint:           model_registry.DefaultHuggingFaceEndpoint,
	}
}

func (o *ModelRegistryOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.EnableBuiltinPublicRegistries, "enable-builtin-public-model-registries",
		o.EnableBuiltinPublicRegistries,
		"provision a built-in read-only model registry for each supported public hub in every workspace")
	fs.StringVar(&o.HuggingFaceEndpoint, "hugging-face-endpoint", o.HuggingFaceEndpoint,
		"address the built-in Hugging Face registry points at, e.g. an in-network mirror")
}
