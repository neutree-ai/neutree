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

// ValidateWritableRegistry refuses a write against a public registry.
//
// A public registry is a catalogue someone else operates: the control plane
// reads it and holds none of its models, so every write against it is answered
// with "operation not supported", and for a push that answer only arrives after
// the whole model has been archived and uploaded. Deciding from the registry the
// command has already fetched turns minutes of wasted transfer into an immediate
// refusal, and turns a transport-level error into a sentence naming the registry
// and what is wrong with it.
//
// Public-or-private is asked of v1.VisibilityForModelRegistryType rather than
// derived here, so that no client carries its own list of which kinds are
// catalogues — the same reason the server asks it in query_cache.go and
// errors.go instead of testing for hugging-face. The next read-only provider is
// refused here as soon as that one mapping learns about it.
//
// The registry object also carries the server's own `visibility`, which is the
// authoritative form of the same answer, but PostgREST omits computed columns
// from `select=*` and so it is absent from what ModelRegistries.Get fetches.
// Reading it would mean teaching that call to select it, which is a pkg/client
// change felt by every caller of it, for an answer the exported mapping already
// gives from one place. Not worth it here; worth revisiting if a caller ever
// needs a visibility the client cannot derive.
//
// This mirrors the server rather than replacing it: a kind the mapping does not
// know is treated as private and falls through to whatever the server answers,
// so the check can only be too permissive, never too strict.
func ValidateWritableRegistry(registry *v1.ModelRegistry, name string, write ModelWrite) error {
	if registry == nil || registry.Spec == nil {
		return nil
	}

	if v1.VisibilityForModelRegistryType(registry.Spec.Type) != v1.ModelRegistryVisibilityPublic {
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
