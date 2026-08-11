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
//
// Reading the kind is the temporary part. The server is growing a `visibility`
// field that states public-or-private itself, so that no client has to know
// which kinds are catalogues; once it lands, switch the condition below to it
// (v1.VisibilityForModelRegistryType is the same judgement in Go, and the
// registry object carries `visibility` when the request selects it — PostgREST
// omits computed columns from `select=*`). Doing so is what makes the next
// read-only provider — ModelScope is the one already coming — refused here
// without anyone remembering to extend a list of kinds.
func ValidateWritableRegistry(registry *v1.ModelRegistry, name string, write ModelWrite) error {
	if registry == nil || registry.Spec == nil {
		return nil
	}

	if registry.Spec.Type != v1.HuggingFaceModelRegistryType {
		return nil
	}

	return fmt.Errorf("cannot %s model registry %q: it is a %s registry, which neutree reads from but never writes to",
		write, registryName(registry, name), registry.Spec.Type)
}

// registryName is what to call the registry in the refusal. It prefers the
// fetched object's own name and falls back to the name the caller asked for,
// because a sentence with empty quotes in it reads as a broken tool rather than
// as a refusal, and the caller always knows what the user typed.
func registryName(registry *v1.ModelRegistry, requested string) string {
	if registry.Metadata != nil && registry.Metadata.Name != "" {
		return registry.Metadata.Name
	}

	return requested
}
