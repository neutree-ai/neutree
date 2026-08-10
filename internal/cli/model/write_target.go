package model

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ModelWrite names a write the CLI is about to attempt, so a refusal can say
// which one was refused rather than "this is not supported".
type ModelWrite string

const (
	ModelWritePush   ModelWrite = "push models to"
	ModelWriteDelete ModelWrite = "delete models from"
)

// ValidateWritableRegistry refuses a write against a registry kind that holds
// no models of ours.
//
// A hugging-face registry is a catalogue the control plane reads; every write
// against it is answered with "operation not supported", and for a push the
// answer only arrives after the whole model has been archived and uploaded.
// Deciding from the registry the command has already fetched turns minutes of
// wasted transfer into an immediate refusal, and turns a transport-level error
// into a sentence naming the registry and what is wrong with it.
//
// This mirrors the server rather than replacing it: a registry kind added later
// still falls through to whatever the server answers, so the check can only be
// too permissive, never too strict.
func ValidateWritableRegistry(registry *v1.ModelRegistry, write ModelWrite) error {
	if registry == nil || registry.Spec == nil {
		return nil
	}

	if registry.Spec.Type != v1.HuggingFaceModelRegistryType {
		return nil
	}

	return fmt.Errorf("cannot %s model registry %q: it is a %s registry, which neutree reads from but never writes to",
		write, registryName(registry), registry.Spec.Type)
}

// registryName is the name the user typed, recovered from the fetched object so
// the message names the registry rather than describing it.
func registryName(registry *v1.ModelRegistry) string {
	if registry.Metadata == nil {
		return ""
	}

	return registry.Metadata.Name
}
