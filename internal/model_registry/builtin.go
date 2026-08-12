package model_registry

import (
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	// BuiltinHuggingFaceRegistryName is the name the built-in public registry is
	// provisioned under, in every workspace that has it.
	BuiltinHuggingFaceRegistryName = "public-hugging-face"
	// DefaultHuggingFaceEndpoint is the Hub itself, used when the built-in
	// registry is enabled without naming a mirror.
	DefaultHuggingFaceEndpoint = "https://huggingface.co"
)

// BuiltinConfig says whether this installation offers public model registries,
// and at which address. Enabled defaults to false so that an installation with
// no route to the internet does not show every user a registry stuck in Failed.
type BuiltinConfig struct {
	// Enabled provisions the built-in public registries.
	Enabled bool
	// HuggingFaceEndpoint is the Hub address to use — a mirror inside the network,
	// or the Hub itself. Empty means DefaultHuggingFaceEndpoint.
	HuggingFaceEndpoint string
}

func (c BuiltinConfig) huggingFaceEndpoint() string {
	endpoint := strings.TrimSpace(c.HuggingFaceEndpoint)
	if endpoint == "" {
		return DefaultHuggingFaceEndpoint
	}

	return strings.TrimSuffix(endpoint, "/")
}

// BuiltinModelRegistries returns the registries this deployment provisions for a
// workspace, or nothing when public registries are disabled.
//
// Every registry it returns carries the built-in annotation, which is the only
// thing distinguishing a provisioned registry from a user's. Provisioning and
// the API's write guard both key off it, so a registry created here without it
// would be adopted from — and editable as — a user's own.
func BuiltinModelRegistries(config BuiltinConfig, workspace string) []*v1.ModelRegistry {
	if !config.Enabled {
		return nil
	}

	return []*v1.ModelRegistry{
		{
			APIVersion: "v1",
			Kind:       "ModelRegistry",
			Metadata: &v1.Metadata{
				Name:        BuiltinHuggingFaceRegistryName,
				Workspace:   workspace,
				Annotations: v1.WithBuiltinAnnotation(nil),
			},
			Spec: &v1.ModelRegistrySpec{
				Type: v1.HuggingFaceModelRegistryType,
				Url:  config.huggingFaceEndpoint(),
			},
		},
	}
}
