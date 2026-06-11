package proxies

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestValidateEndpointAcceleratorVirtualizationBody(t *testing.T) {
	t.Run("allows non vGPU endpoint resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("allows raw accelerator keys without virtualization fields", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"nvidia.com/gpucores": "50"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("rejects vGPU endpoint without product", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"virtualization.memory_mib": "8192"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10218", err.Code)
	})

	t.Run("rejects mutually exclusive memory fields", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.memory_percent": "50"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10219", err.Code)
	})

	t.Run("rejects invalid vGPU numeric resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.core_percent": "101"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
	})

	t.Run("allows vGPU endpoint resource shape without cluster availability lookup", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192",
						"virtualization.core_percent": "50"
					}
				}
			}
		}`))

		assert.Nil(t, err)
	})

	t.Run("skips patch that does not touch resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"spec": {
				"replicas": {"num": 2}
			}
		}`))

		assert.Nil(t, err)
	})
}
