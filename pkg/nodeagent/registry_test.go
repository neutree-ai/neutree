package nodeagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type registryTestAdapter struct {
	typ         string
	descriptors []adapter.MetricDescriptor
}

func (a registryTestAdapter) Type() string { return a.typ }

func (registryTestAdapter) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{}, nil
}

func (a registryTestAdapter) MetricDescriptors() []adapter.MetricDescriptor {
	return adapter.CloneMetricDescriptors(a.descriptors)
}

func TestNewAdapterRegistryRejectsInvalidAdapterSlices(t *testing.T) {
	typedNil := (*registryTestAdapter)(nil)
	testCases := []struct {
		name          string
		adapters      []adapter.Accelerator
		expectedError string
	}{
		{name: "nil", adapters: []adapter.Accelerator{nil}, expectedError: "adapter is nil"},
		{name: "typed nil", adapters: []adapter.Accelerator{typedNil}, expectedError: "adapter is nil"},
		{name: "empty type", adapters: []adapter.Accelerator{registryTestAdapter{}}, expectedError: "type is required"},
		{
			name: "duplicate type",
			adapters: []adapter.Accelerator{
				registryTestAdapter{typ: "vendor"},
				registryTestAdapter{typ: "vendor"},
			},
			expectedError: `duplicate accelerator adapter "vendor"`,
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			_, err := newAdapterRegistry(testCase.adapters)
			assert.ErrorContains(t, err, testCase.expectedError)
		})
	}
}

func TestNewAdapterRegistryRejectsDescriptorConflicts(t *testing.T) {
	_, err := newAdapterRegistry([]adapter.Accelerator{
		registryTestAdapter{
			typ: "vendor-a",
			descriptors: []adapter.MetricDescriptor{{
				Name:       "neutree_vendor_metric",
				LabelNames: []string{"device"},
			}},
		},
		registryTestAdapter{
			typ: "vendor-b",
			descriptors: []adapter.MetricDescriptor{{
				Name:       "neutree_vendor_metric",
				LabelNames: []string{"device"},
			}},
		},
	})

	assert.ErrorContains(t, err, `descriptor "neutree_vendor_metric" conflicts`)
}

func TestDefaultAdaptersReturnsFreshSliceAndInstances(t *testing.T) {
	first := DefaultAdapters()
	second := DefaultAdapters()

	assert.Empty(t, first)
	assert.Empty(t, second)
	first = append(first, registryTestAdapter{typ: "mutated"})
	assert.Empty(t, second)
}
