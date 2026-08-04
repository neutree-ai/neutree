// Package admission defines the public contract for resource admission hooks.
package admission

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
)

// Operation identifies the write operation a hook handles.
type Operation uint8

const (
	// UnknownOperation is not valid for hook registration.
	UnknownOperation Operation = iota
	// Create handles POST candidate objects.
	Create
	// Update handles ordinary PATCH candidate objects.
	Update
	// Delete handles soft-delete PATCH candidate objects.
	Delete
)

// Phase identifies a hook's position in an admission chain.
type Phase uint8

const (
	// UnknownPhase is not valid for hook registration.
	UnknownPhase Phase = iota
	// Mutating hooks run before validation hooks.
	Mutating
	// Validating hooks inspect the final candidate object.
	Validating
)

// HookMeta identifies a hook and determines its execution order.
type HookMeta struct {
	Name      string
	Operation Operation
	Phase     Phase
	Order     int
}

// Resource describes a write resource and its decoded object type.
//
// The type parameter is intentionally used only while hooks are registered;
// the registry exposes a type-erased Chain to the HTTP runner.
type Resource[T any] struct {
	Name string
}

// NewResource creates a typed resource descriptor.
func NewResource[T any](name string) Resource[T] {
	return Resource[T]{Name: name}
}

func (r Resource[T]) admissionResourceDescriptor() resourceDescriptor {
	return resourceDescriptor{Name: r.Name, ObjectType: reflect.TypeFor[T]()}
}

// RequestContext carries request-scoped values provided by the admission
// runner. It intentionally does not expose storage or transport clients.
type RequestContext struct {
	Context context.Context
}

// Mutator returns the replacement candidate to pass to the next hook.
type Mutator[T any] func(RequestContext, T) (T, error)

// CreateValidator validates a create candidate.
type CreateValidator[T any] func(RequestContext, T) error

// UpdateValidator validates an old object and its update candidate.
type UpdateValidator[T any] func(RequestContext, T, T) error

// DeleteValidator validates an old object and its soft-delete candidate.
type DeleteValidator[T any] func(RequestContext, T, T) error

// Hook is a registered, type-erased admission hook. Construct it with the
// typed constructor matching the operation and phase.
type Hook struct {
	meta     HookMeta
	code     int
	mutate   func(RequestContext, any) (any, error)
	validate func(RequestContext, any, any) error
}

// Meta returns the hook metadata supplied to its typed constructor.
func (h Hook) Meta() HookMeta {
	return h.meta
}

// Code returns the numeric error code owned by the hook.
func (h Hook) Code() int {
	return h.code
}

// MutateCreate creates a create mutation hook.
func MutateCreate[T any](meta HookMeta, code int, fn Mutator[T]) Hook {
	return mutateHook(meta, code, Create, fn)
}

// MutateUpdate creates an update mutation hook. The first-release registry
// rejects it until a resource can safely translate a replacement object to a patch.
func MutateUpdate[T any](meta HookMeta, code int, fn Mutator[T]) Hook {
	return mutateHook(meta, code, Update, fn)
}

// ValidateCreate creates a create validation hook.
func ValidateCreate[T any](meta HookMeta, code int, fn CreateValidator[T]) Hook {
	meta.Operation = Create
	meta.Phase = Validating
	return Hook{
		meta: meta,
		code: code,
		validate: func(ctx RequestContext, _, candidate any) error {
			value, err := snapshotAs[T](candidate)
			if err != nil {
				return err
			}
			return fn(ctx, value)
		},
	}
}

// ValidateUpdate creates an update validation hook.
func ValidateUpdate[T any](meta HookMeta, code int, fn UpdateValidator[T]) Hook {
	return validateOldAndCandidateHook(meta, code, Update, fn)
}

// ValidateDelete creates a soft-delete validation hook.
func ValidateDelete[T any](meta HookMeta, code int, fn DeleteValidator[T]) Hook {
	return validateOldAndCandidateHook(meta, code, Delete, fn)
}

// MutateCreateHook is a compatibility spelling for MutateCreate.
func MutateCreateHook[T any](meta HookMeta, code int, fn Mutator[T]) Hook {
	return MutateCreate(meta, code, fn)
}

// MutateUpdateHook is a compatibility spelling for MutateUpdate.
func MutateUpdateHook[T any](meta HookMeta, code int, fn Mutator[T]) Hook {
	return MutateUpdate(meta, code, fn)
}

// ValidateCreateHook is a compatibility spelling for ValidateCreate.
func ValidateCreateHook[T any](meta HookMeta, code int, fn CreateValidator[T]) Hook {
	return ValidateCreate(meta, code, fn)
}

// ValidateUpdateHook is a compatibility spelling for ValidateUpdate.
func ValidateUpdateHook[T any](meta HookMeta, code int, fn UpdateValidator[T]) Hook {
	return ValidateUpdate(meta, code, fn)
}

// ValidateDeleteHook is a compatibility spelling for ValidateDelete.
func ValidateDeleteHook[T any](meta HookMeta, code int, fn DeleteValidator[T]) Hook {
	return ValidateDelete(meta, code, fn)
}

func mutateHook[T any](meta HookMeta, code int, operation Operation, fn Mutator[T]) Hook {
	meta.Operation = operation
	meta.Phase = Mutating
	return Hook{
		meta: meta,
		code: code,
		mutate: func(ctx RequestContext, candidate any) (any, error) {
			value, ok := candidate.(T)
			if !ok {
				return nil, typeMismatch[T](candidate)
			}
			return fn(ctx, value)
		},
	}
}

func validateOldAndCandidateHook[T any](meta HookMeta, code int, operation Operation, fn func(RequestContext, T, T) error) Hook {
	meta.Operation = operation
	meta.Phase = Validating
	return Hook{
		meta: meta,
		code: code,
		validate: func(ctx RequestContext, old, candidate any) error {
			oldValue, err := snapshotAs[T](old)
			if err != nil {
				return err
			}
			candidateValue, err := snapshotAs[T](candidate)
			if err != nil {
				return err
			}
			return fn(ctx, oldValue, candidateValue)
		},
	}
}

func snapshotAs[T any](value any) (T, error) {
	var copy T
	encoded, err := json.Marshal(value)
	if err != nil {
		return copy, fmt.Errorf("snapshot admission candidate: %w", err)
	}
	if err := json.Unmarshal(encoded, &copy); err != nil {
		return copy, fmt.Errorf("decode admission candidate snapshot: %w", err)
	}
	return copy, nil
}

func typeMismatch[T any](value any) error {
	return fmt.Errorf("admission candidate has type %T, expected %T", value, *new(T))
}
