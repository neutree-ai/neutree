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

// BuiltinConfig is the deployment's answer to "should this installation offer
// public model registries, and against which address".
//
// Enabled defaults to false, and that default is the point. An installation with
// no route to the internet that shipped a public registry switched on would
// present every user with a registry stuck in Failed, which reads as a broken
// deployment rather than as a feature nobody asked for.
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
// workspace: the Hugging Face one when public registries are enabled, and
// nothing at all when they are not.
//
// Every registry it returns carries the built-in annotation. That is what tells
// a control-plane-provisioned registry from one a user created — which is what
// keeps provisioning from adopting somebody's registry that happens to share the
// name, and what lets the API refuse the edits that do not make sense on a
// registry this deployment manages.
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
