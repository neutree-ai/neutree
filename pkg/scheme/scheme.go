package scheme

import (
	"encoding/json"
	"fmt"
	"reflect"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Scheme defines a simple type registry for mapping VersionKind to Go types.
type Scheme struct {
	vkToType map[string]reflect.Type
	typeToVK map[reflect.Type]string
}

// NewScheme creates a new Scheme.
func NewScheme() *Scheme {
	return &Scheme{
		vkToType: make(map[string]reflect.Type),
		typeToVK: make(map[reflect.Type]string),
	}
}

// AddKnownTypes registers the given types with the Scheme for a specific version.
func (s *Scheme) AddKnownTypes(types ...v1.Object) {
	for _, obj := range types {
		t := reflect.TypeOf(obj)
		if t.Kind() == reflect.Ptr {
			t = t.Elem()
		}
		kind := t.Name()

		s.vkToType[kind] = t
		s.typeToVK[t] = kind
	}
}

// New creates a new v1.Object instance from a version and kind.
func (s *Scheme) New(kind string) (v1.Object, error) {
	t, ok := s.vkToType[kind]
	if !ok {
		return nil, fmt.Errorf("unregistered type: %s", kind)
	}

	instance := reflect.New(t).Interface()
	obj, ok := instance.(v1.Object)
	if !ok {
		return nil, fmt.Errorf("type %T does not implement v1.Object", instance)
	}

	obj.SetKind(kind)
	return obj, nil
}

// CodecFactory is a simplified factory for creating decoders.
type CodecFactory struct {
	scheme *Scheme
}

// NewCodecFactory creates a new CodecFactory for the given Scheme.
func NewCodecFactory(scheme *Scheme) *CodecFactory {
	return &CodecFactory{scheme: scheme}
}

// Decoder returns a decoder that can decode registered types.
func (f *CodecFactory) Decoder() Decoder {
	return &decoder{scheme: f.scheme}
}

type Decoder interface {
	Decode(data []byte, defaultKind string) (v1.Object, error)
}

type decoder struct {
	scheme *Scheme
}

// Decode deserializes JSON data into a registered v1.Object.
func (d *decoder) Decode(data []byte, defaultKind string) (v1.Object, error) {
	var k struct {
		Kind string `json:"kind,omitempty"`
	}
	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("failed to unmarshal kind: %w", err)
	}

	if k.Kind == "" {
		k.Kind = defaultKind
	}

	obj, err := d.scheme.New(k.Kind)
	if err != nil {
		return nil, err
	}

	if err := json.Unmarshal(data, obj); err != nil {
		return nil, err
	}

	return obj, nil
}

// SchemeBuilder collects functions that add types to a scheme.
type SchemeBuilder []func(*Scheme) error

// AddToScheme applies all the stored functions to the given scheme.
func (sb *SchemeBuilder) AddToScheme(s *Scheme) error {
	for _, f := range *sb {
		if err := f(s); err != nil {
			return err
		}
	}
	return nil
}

// Register adds a new function to the SchemeBuilder.
func (sb *SchemeBuilder) Register(funcs ...func(*Scheme) error) {
	*sb = append(*sb, funcs...)
}

// Builder builds a new Scheme for mapping go types to Neutree Kinds.
type Builder struct {
	SchemeBuilder
}

// Register adds one or more objects to the SchemeBuilder so they can be added to a Scheme.  Register mutates bld.
func (bld *Builder) Register(object ...v1.Object) *Builder {
	bld.SchemeBuilder.Register(func(scheme *Scheme) error {
		scheme.AddKnownTypes(object...)
		return nil
	})
	return bld
}
