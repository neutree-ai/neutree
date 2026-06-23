package proxies

import (
	"strconv"
	"testing"

	"github.com/stretchr/testify/assert"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
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

	t.Run("rejects fractional vGPU memory resource", func(t *testing.T) {
		err := validateEndpointAcceleratorVirtualizationBody([]byte(`{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "cluster",
				"resources": {
					"gpu": "1",
					"accelerator": {
						"type": "nvidia_gpu",
						"product": "Tesla-T4",
						"virtualization.memory_mib": "8192.5",
						"virtualization.core_percent": "50"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.memory_mib")
		assert.Contains(t, err.Hint, "positive integer")
	})

	t.Run("rejects fractional vGPU core resource", func(t *testing.T) {
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
						"virtualization.core_percent": "50.5"
					}
				}
			}
		}`))

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
		assert.Contains(t, err.Hint, "positive integer")
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

func TestValidateEndpointAcceleratorVirtualizationClusterReadiness(t *testing.T) {
	body := []byte(`{
		"metadata": {"name": "endpoint", "workspace": "default"},
		"spec": {
			"cluster": "cluster",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "1024",
					"virtualization.core_percent": "10"
				}
			}
		}
	}`)

	t.Run("rejects vGPU endpoint when cluster virtualization component is not ready", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", endpointClusterListOption()).Return([]v1.Cluster{{
			Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
			Spec: &v1.ClusterSpec{
				Type:                      v1.KubernetesClusterType,
				AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
			},
			Status: &v1.ClusterStatus{
				ComponentStatus: map[string]*v1.ComponentStatus{
					v1.ComponentStatusAcceleratorVirtualizationKey: {
						Phase:   v1.ComponentPhaseNotReady,
						Reason:  "DaemonSetNotReady",
						Message: "waiting for device plugin",
					},
				},
			},
		}}, nil)

		err := validateEndpointAcceleratorVirtualizationClusterReadiness(body, &Dependencies{Storage: mockStorage})

		assert.NotNil(t, err)
		assert.Equal(t, "10221", err.Code)
		assert.Contains(t, err.Hint, "accelerator virtualization component is not ready")
	})

	t.Run("rejects vGPU endpoint when cluster has no available vGPU resource", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", endpointClusterListOption()).Return([]v1.Cluster{readyVirtualizationCluster(nil)}, nil)

		err := validateEndpointAcceleratorVirtualizationClusterReadiness(body, &Dependencies{Storage: mockStorage})

		assert.NotNil(t, err)
		assert.Equal(t, "10221", err.Code)
		assert.Contains(t, err.Hint, "has no available vGPU resource")
	})

	t.Run("allows vGPU endpoint when cluster virtualization is ready and available", func(t *testing.T) {
		mockStorage := storageMocks.NewMockStorage(t)
		mockStorage.On("ListCluster", endpointClusterListOption()).Return([]v1.Cluster{readyVirtualizationCluster(&v1.ClusterResources{
			ResourceStatus: v1.ResourceStatus{
				Available: &v1.ResourceInfo{
					AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
						v1.AcceleratorTypeNVIDIAGPU: {
							Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
								"Tesla-T4": {
									Quantity: 1,
									Virtualization: &v1.AcceleratorVirtualizationResource{
										MemoryMiB: 1024,
										CoreUnits: 10,
									},
								},
							},
						},
					},
				},
			},
		})}, nil)

		err := validateEndpointAcceleratorVirtualizationClusterReadiness(body, &Dependencies{Storage: mockStorage})

		assert.Nil(t, err)
	})
}

func endpointClusterListOption() storage.ListOption {
	return storage.ListOption{Filters: []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: strconv.Quote("cluster")},
		{Column: "metadata->workspace", Operator: "eq", Value: strconv.Quote("default")},
	}}
}

func readyVirtualizationCluster(resources *v1.ClusterResources) v1.Cluster {
	return v1.Cluster{
		Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
		Spec: &v1.ClusterSpec{
			Type:                      v1.KubernetesClusterType,
			AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{Enabled: true},
		},
		Status: &v1.ClusterStatus{
			ComponentStatus: map[string]*v1.ComponentStatus{
				v1.ComponentStatusAcceleratorVirtualizationKey: {Phase: v1.ComponentPhaseReady},
			},
			ResourceInfo: resources,
		},
	}
}
