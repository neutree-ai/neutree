package app

import (
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

// adapterRegistry is intentionally private. It freezes the entrypoint's
// adapter slice into a lookup table before the NodeAgent starts.
type adapterRegistry struct {
	byType      map[string]adapter.Accelerator
	descriptors []adapter.MetricDescriptor
}

func newAdapterRegistry(accelerators []adapter.Accelerator) (adapterRegistry, error) {
	registry := adapterRegistry{
		byType: make(map[string]adapter.Accelerator, len(accelerators)),
	}
	descriptorOwners := make(map[string]string)

	for _, accelerator := range accelerators {
		if isNilAdapter(accelerator) {
			return adapterRegistry{}, fmt.Errorf("accelerator adapter is nil")
		}

		acceleratorType := strings.TrimSpace(accelerator.Type())
		if acceleratorType == "" {
			return adapterRegistry{}, fmt.Errorf("accelerator adapter type is required")
		}

		if _, exists := registry.byType[acceleratorType]; exists {
			return adapterRegistry{}, fmt.Errorf("duplicate accelerator adapter %q", acceleratorType)
		}

		registry.byType[acceleratorType] = accelerator

		provider, ok := accelerator.(adapter.MetricDescriptorProvider)
		if !ok {
			continue
		}

		for _, descriptor := range provider.MetricDescriptors() {
			if err := validateAdapterDescriptor(acceleratorType, descriptor); err != nil {
				return adapterRegistry{}, err
			}

			name := strings.TrimSpace(descriptor.Name)
			if owner, exists := descriptorOwners[name]; exists {
				return adapterRegistry{}, fmt.Errorf(
					"accelerator metric descriptor %q conflicts between adapters %q and %q",
					name,
					owner,
					acceleratorType,
				)
			}

			descriptorOwners[name] = acceleratorType

			registry.descriptors = append(registry.descriptors, adapter.MetricDescriptor{
				Name:               name,
				LabelNames:         append([]string(nil), descriptor.LabelNames...),
				RequiredLabelNames: append([]string(nil), descriptor.RequiredLabelNames...),
			})
		}
	}

	sort.Slice(registry.descriptors, func(i, j int) bool {
		return registry.descriptors[i].Name < registry.descriptors[j].Name
	})

	return registry, nil
}

func (r adapterRegistry) accelerators() map[string]adapter.Accelerator {
	result := make(map[string]adapter.Accelerator, len(r.byType))
	for acceleratorType, accelerator := range r.byType {
		result[acceleratorType] = accelerator
	}

	return result
}

func (r adapterRegistry) descriptorsCopy() []adapter.MetricDescriptor {
	return adapter.CloneMetricDescriptors(r.descriptors)
}

func (r adapterRegistry) types() []string {
	types := make([]string, 0, len(r.byType))
	for acceleratorType := range r.byType {
		types = append(types, acceleratorType)
	}

	sort.Strings(types)

	return types
}

func validateAdapterDescriptor(acceleratorType string, descriptor adapter.MetricDescriptor) error {
	name := strings.TrimSpace(descriptor.Name)
	if name == "" {
		return fmt.Errorf("accelerator adapter %q declares an empty metric descriptor name", acceleratorType)
	}

	labels := make(map[string]struct{}, len(descriptor.LabelNames))

	for _, label := range descriptor.LabelNames {
		label = strings.TrimSpace(label)
		if label == "" {
			return fmt.Errorf("accelerator metric descriptor %q has an empty label name", name)
		}

		if _, exists := labels[label]; exists {
			return fmt.Errorf("accelerator metric descriptor %q has duplicate label %q", name, label)
		}

		labels[label] = struct{}{}
	}

	required := make(map[string]struct{}, len(descriptor.RequiredLabelNames))

	for _, label := range descriptor.RequiredLabelNames {
		label = strings.TrimSpace(label)
		if label == "" {
			return fmt.Errorf("accelerator metric descriptor %q has an empty required label name", name)
		}

		if _, exists := labels[label]; !exists {
			return fmt.Errorf("accelerator metric descriptor %q requires unknown label %q", name, label)
		}

		if _, exists := required[label]; exists {
			return fmt.Errorf("accelerator metric descriptor %q has duplicate required label %q", name, label)
		}

		required[label] = struct{}{}
	}

	return nil
}

func isNilAdapter(accelerator adapter.Accelerator) bool {
	if accelerator == nil {
		return true
	}

	value := reflect.ValueOf(accelerator)
	switch value.Kind() {
	case reflect.Chan, reflect.Func, reflect.Interface, reflect.Map, reflect.Pointer, reflect.Slice, reflect.UnsafePointer:
		return value.IsNil()
	default:
		return false
	}
}
