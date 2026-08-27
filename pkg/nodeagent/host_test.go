package nodeagent

import (
	"context"
	"testing"

	"github.com/stretchr/testify/assert"

	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

func TestRunRejectsBuiltInDescriptorConflictBeforeVersionCommand(t *testing.T) {
	err := Run(context.Background(), Config{
		Args: []string{"version"},
		Adapters: []adapter.Accelerator{registryTestAdapter{
			typ: "vendor",
			descriptors: []adapter.MetricDescriptor{{
				Name: "neutree_node_ready",
			}},
		}},
	})

	assert.ErrorContains(t, err, `conflicts with an existing descriptor`)
}

func TestConfigWithRegistryRejectsUnknownExplicitAdapter(t *testing.T) {
	registry, err := newAdapterRegistry([]adapter.Accelerator{registryTestAdapter{typ: "vendor"}})
	if !assert.NoError(t, err) {
		return
	}
	opts := newOptions()
	opts.clusterType = clusterTypeRay
	opts.acceleratorType = "unknown"

	_, err = opts.configWithRegistry(registry)

	assert.ErrorContains(t, err, `accelerator adapter "unknown" is not registered`)
}

func TestValidateSelectedAdapterCapabilityFailsBeforeProviderSetup(t *testing.T) {
	registry, err := newAdapterRegistry([]adapter.Accelerator{registryTestAdapter{typ: "vendor"}})
	if !assert.NoError(t, err) {
		return
	}

	err = validateSelectedAdapterCapability(clusterTypeKubernetes, "vendor", registry)

	assert.ErrorContains(t, err, `accelerator adapter "vendor" does not implement Kubernetes capability`)
}
