package scheme

import (
	"encoding/json"
	"fmt"
	"reflect"
	"strings"
)

// Scheme defines a simple type registry for mapping VersionKind to Go types.
type Scheme struct {
	vkToType    map[string]reflect.Type
	typeToVK    map[reflect.Type]string
	tableToKind map[string]string
	kindToTable map[string]string
}

// NewScheme creates a new Scheme.
func NewScheme() *Scheme {
	return &Scheme{
		vkToType:    make(map[string]reflect.Type),
		typeToVK:    make(map[reflect.Type]string),
		tableToKind: make(map[string]string),
		kindToTable: make(map[string]string),
	}
}

// AddKnownTypes registers the given types with the Scheme for a specific version.
func (s *Scheme) AddKnownTypes(types ...ObjectKind) {
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

// AddKnownTableTypes registers mappings from table names to their singular kind.
func (s *Scheme) AddKnownTableTypes(tableToKind map[string]string) {
	for table, kind := range tableToKind {
		s.tableToKind[table] = kind
		s.kindToTable[kind] = table
	}
}

// New creates a new v1.Object instance from a version and kind.
func (s *Scheme) New(kind string) (Object, error) {
	t, originalKind, err := s.gvkToType(kind)
	if err != nil {
		return nil, err
	}

	instance := reflect.New(t).Interface()
	obj, ok := instance.(Object)

	if !ok {
		return nil, fmt.Errorf("type %T does not implement Object", instance)
	}

	obj.SetKind(originalKind)

	return obj, nil
}

// New creates a new v1.ObjectList instance from a version and kind.
func (s *Scheme) NewList(kind string) (ObjectList, error) {
	t, originalKind, err := s.gvkToType(kind)
	if err != nil {
		return nil, err
	}

	instance := reflect.New(t).Interface()
	obj, ok := instance.(ObjectList)

	if !ok {
		return nil, fmt.Errorf("type %T does not implement ObjectList", instance)
	}

	obj.SetKind(originalKind)

	return obj, nil
}

// gvkToType returns the type and original kind for a given kind string.
func (s *Scheme) gvkToType(kind string) (reflect.Type, string, error) {
	t, ok := s.vkToType[kind]
	if !ok {
		tableKind, isPlural := s.tableToKind[kind]
		if !isPlural {
			return nil, "", fmt.Errorf("unregistered type: %s", kind)
		}

		t, ok = s.vkToType[tableKind]
		if !ok {
			return nil, "", fmt.Errorf("unregistered type for kind %s (from table %s)", tableKind, kind)
		}

		return t, tableKind, nil
	}

	return t, kind, nil
}

// ObjectKind returns the kind of the given object.
func (s *Scheme) ObjectKind(obj ObjectKind) string {
	t := reflect.TypeOf(obj)
	if t.Kind() == reflect.Ptr {
		t = t.Elem()
	}

	return t.Name()
}

// KindToTable returns the table name for a given kind, if registered.
func (s *Scheme) KindToTable(kind string) (string, bool) {
	table, ok := s.kindToTable[kind]
	return table, ok
}

// ResolveKind resolves a user input string to a canonical kind name.
// It handles exact kind match, table name match, and case-insensitive matching.
func (s *Scheme) ResolveKind(input string) (string, bool) {
	// Exact kind match (e.g., "Endpoint")
	if _, ok := s.kindToTable[input]; ok {
		return input, true
	}

	// Exact table name match (e.g., "endpoints")
	if kind, ok := s.tableToKind[input]; ok {
		return kind, true
	}

	// Case-insensitive kind match (e.g., "endpoint" → "Endpoint")
	lower := strings.ToLower(input)
	for kind := range s.kindToTable {
		if strings.ToLower(kind) == lower {
			return kind, true
		}
	}

	// Case-insensitive table match (e.g., "Endpoints" → "Endpoint")
	for table, kind := range s.tableToKind {
		if strings.ToLower(table) == lower {
			return kind, true
		}
	}

	return "", false
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
	Decode(data []byte, defaultKind string) (Object, error)
}

type decoder struct {
	scheme *Scheme
}

// Decode deserializes JSON data into a registered v1.Object.
func (d *decoder) Decode(data []byte, defaultKind string) (Object, error) {
	var k struct {
		Kind string `json:"kind,omitempty"`
	}

	if err := json.Unmarshal(data, &k); err != nil {
		return nil, fmt.Errorf("failed to unmarshal kind: %w", err)
	}

	if k.Kind == "" {
		k.Kind = defaultKind
	}

	if k.Kind == "" {
		return nil, fmt.Errorf("kind is required")
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
func (bld *Builder) Register(objectKind ...ObjectKind) *Builder {
	bld.SchemeBuilder.Register(func(scheme *Scheme) error {
		scheme.AddKnownTypes(objectKind...)
		return nil
	})

	return bld
}

// RegisterTable adds one or more table mappings to the SchemeBuilder.
func (bld *Builder) RegisterTable(tableToKind map[string]string) *Builder {
	bld.SchemeBuilder.Register(func(scheme *Scheme) error {
		scheme.AddKnownTableTypes(tableToKind)
		return nil
	})

	return bld
}
