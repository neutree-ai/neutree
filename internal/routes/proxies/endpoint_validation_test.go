package proxies

import (
	"net/http"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateEndpointAcceleratorVirtualizationBody(t *testing.T) {
	t.Run("allows non vGPU endpoint resources", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
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
		}`), "", nil)

		assert.Nil(t, err)
	})

	t.Run("rejects raw HAMi resource keys", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
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
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
	})

	t.Run("rejects vGPU endpoint without product", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
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
		}`), "", nil)

		assert.NotNil(t, err)
		assert.Equal(t, "10218", err.Code)
	})

	t.Run("rejects vGPU endpoint when memory exceeds available pool", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: `"cluster"`},
				{Column: "metadata->workspace", Operator: "eq", Value: `"default"`},
			},
		}).Return([]v1.Cluster{endpointValidationCluster(2, 7168, 100, 15360)}, nil)

		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
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
		}`), "", mockStorage)

		assert.NotNil(t, err)
		assert.Equal(t, "10223", err.Code)
		assert.Contains(t, err.Message, "available vGPU memory")
		mockStorage.AssertExpectations(t)
	})

	t.Run("allows vGPU endpoint when requested resources are available", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: `"cluster"`},
				{Column: "metadata->workspace", Operator: "eq", Value: `"default"`},
			},
		}).Return([]v1.Cluster{endpointValidationCluster(2, 8192, 100, 15360)}, nil)

		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPost, []byte(`{
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
		}`), "", mockStorage)

		assert.Nil(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("loads existing endpoint for partial resource patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListEndpoint", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: "endpoint"},
				{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			},
		}).Return([]v1.Endpoint{
			{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"},
				Spec:     &v1.EndpointSpec{Cluster: "cluster"},
			},
		}, nil)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: `"cluster"`},
				{Column: "metadata->workspace", Operator: "eq", Value: `"default"`},
			},
		}).Return([]v1.Cluster{endpointValidationCluster(2, 8192, 100, 15360)}, nil)

		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPatch, []byte(`{
			"spec": {
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
		}`), "metadata->>name=eq.endpoint&metadata->>workspace=eq.default", mockStorage)

		assert.Nil(t, err)
		mockStorage.AssertExpectations(t)
	})

	t.Run("reclaims existing endpoint allocation for resource patch", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListEndpoint", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->>name", Operator: "eq", Value: "endpoint"},
				{Column: "metadata->>workspace", Operator: "eq", Value: "default"},
			},
		}).Return([]v1.Endpoint{
			{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"},
				Spec:     &v1.EndpointSpec{Cluster: "cluster"},
				Status: &v1.EndpointStatus{
					Resources: &v1.EndpointResourceStatus{
						Replicas: []v1.ReplicaDeviceAllocation{
							{
								Devices: []v1.DeviceAllocation{
									{
										UUID:      "GPU-1",
										Product:   "Tesla-T4",
										MemoryMiB: 8192,
										CoreUnits: 50,
									},
								},
							},
						},
					},
				},
			},
		}, nil)
		mockStorage.On("ListCluster", storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: `"cluster"`},
				{Column: "metadata->workspace", Operator: "eq", Value: `"default"`},
			},
		}).Return([]v1.Cluster{endpointValidationCluster(0, 0, 0, 15360)}, nil)

		err := validateEndpointAcceleratorVirtualizationBody(http.MethodPatch, []byte(`{
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
		}`), "metadata->>name=eq.endpoint&metadata->>workspace=eq.default", mockStorage)

		assert.Nil(t, err)
		mockStorage.AssertExpectations(t)
	})
}

func endpointValidationCluster(quantity, memoryMiB, coreUnits, memoryTotalMiB float64) v1.Cluster {
	return v1.Cluster{
		Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
		Status: &v1.ClusterStatus{
			ResourceInfo: &v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
									"Tesla-T4": {
										Quantity: quantity,
										Virtualization: &v1.AcceleratorVirtualizationResource{
											MemoryMiB: memoryMiB,
											CoreUnits: coreUnits,
										},
									},
								},
							},
						},
					},
				},
				AcceleratorMetadata: map[v1.AcceleratorType]*v1.AcceleratorMetadata{
					v1.AcceleratorTypeNVIDIAGPU: {
						Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductMetadata{
							"Tesla-T4": {
								MemoryTotalMiB: memoryTotalMiB,
							},
						},
					},
				},
			},
		},
	}
}
