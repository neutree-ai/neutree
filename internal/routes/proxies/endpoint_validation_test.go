package proxies

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
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

func TestValidateEndpointAcceleratorVirtualizationCapacity(t *testing.T) {
	t.Run("skips when cluster resource info is unavailable", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := &v1.Cluster{
			Status: &v1.ClusterStatus{},
		}

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("skips when available accelerator telemetry is incomplete", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := &v1.Cluster{
			Status: &v1.ClusterStatus{
				ResourceInfo: &v1.ClusterResources{},
			},
		}

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects request that exceeds per device memory availability", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8193",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "Tesla-T4")
		assert.Contains(t, err.Hint, "satisfiable")
	})

	t.Run("rejects product absent from cluster resource info", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Missing-GPU", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "Missing-GPU")
	})

	t.Run("skips when product exists but virtualization telemetry is missing", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})
		cluster.Status.ResourceInfo.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].
			Products[v1.AcceleratorProduct("Tesla-T4")].Virtualization = nil

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects fragmented capacity that cannot satisfy each requested virtual card", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8193",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 32768, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
			healthyDevice("gpu-1", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_gpu=2")
	})

	t.Run("rejects request that exceeds per device core availability", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "51",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 50),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_core_units=51")
		assert.Contains(t, err.Hint, "satisfiable_devices=0")
	})

	t.Run("rejects request that needs more healthy matching devices than available", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_gpu=2")
		assert.Contains(t, err.Hint, "satisfiable_devices=1")
	})

	t.Run("ignores unhealthy matching devices", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 32768, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
			unhealthyDevice("gpu-1", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "satisfiable_devices=1")
	})

	t.Run("allows request when enough healthy matching devices fit", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 50),
			healthyDevice("gpu-1", "Tesla-T4", 8192, 50),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("derives memory from memory percent using product metadata", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "51",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 10001, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 5101, 100),
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("skips memory percent precheck when product memory metadata is missing", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "51",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 10001, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 5101, 100),
		})
		cluster.Status.ResourceInfo.AcceleratorMetadata = nil

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("skips when matching device availability telemetry is incomplete", func(t *testing.T) {
		resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		device := healthyDevice("gpu-0", "Tesla-T4", 8192, 100)
		device.Available = nil
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			device,
		})

		err := validateEndpointAcceleratorVirtualizationCapacity(resources, cluster)

		assert.Nil(t, err)
	})
}

func TestValidateEndpointAcceleratorVirtualizationCreateCapacitySkipsWhenClusterCannotBePrevalidated(t *testing.T) {
	t.Run("skips when cluster storage is unavailable", func(t *testing.T) {
		endpoint := endpointWithAcceleratorVirtualization("cluster-a", "team-a")

		err := validateEndpointAcceleratorVirtualizationCreateCapacity(nil, endpoint)

		assert.Nil(t, err)
	})

	t.Run("skips when endpoint has no target cluster", func(t *testing.T) {
		clusterStorage := &fakeClusterStorage{
			listError: errors.New("unexpected lookup"),
		}
		endpoint := endpointWithAcceleratorVirtualization("", "team-a")

		err := validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)

		assert.Nil(t, err)
		assert.Equal(t, 0, clusterStorage.listCalls)
	})

	t.Run("skips when endpoint workspace is missing", func(t *testing.T) {
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 1024, 100),
		})
		clusterStorage := &fakeClusterStorage{
			clusters: []v1.Cluster{*cluster},
		}
		endpoint := endpointWithAcceleratorVirtualization("cluster-a", "")

		err := validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)

		assert.Nil(t, err)
		assert.Equal(t, 0, clusterStorage.listCalls)
	})

	t.Run("skips when cluster lookup fails", func(t *testing.T) {
		clusterStorage := &fakeClusterStorage{
			listError: errors.New("storage unavailable"),
		}
		endpoint := endpointWithAcceleratorVirtualization("cluster-a", "team-a")

		err := validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)

		assert.Nil(t, err)
		assert.Equal(t, 1, clusterStorage.listCalls)
	})

	t.Run("skips when cluster is not found", func(t *testing.T) {
		clusterStorage := &fakeClusterStorage{}
		endpoint := endpointWithAcceleratorVirtualization("cluster-a", "team-a")

		err := validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)

		assert.Nil(t, err)
		assert.Equal(t, 1, clusterStorage.listCalls)
	})
}

func TestValidateEndpointAcceleratorVirtualizationCreateCapacityLooksUpClusterByNameAndWorkspace(t *testing.T) {
	resources := acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
		v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
		v1.AcceleratorVirtualizationCorePercentKey: "50",
	})
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
	})
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
	}
	endpoint := &v1.Endpoint{
		Metadata: &v1.Metadata{
			Name:      "endpoint",
			Workspace: "team-a",
		},
		Spec: &v1.EndpointSpec{
			Cluster:   "cluster-a",
			Resources: resources,
		},
	}

	err := validateEndpointAcceleratorVirtualizationCreateCapacity(clusterStorage, endpoint)

	assert.Nil(t, err)
	assert.Equal(t, []storage.Filter{
		{Column: "metadata->name", Operator: "eq", Value: `"cluster-a"`},
		{Column: "metadata->workspace", Operator: "eq", Value: `"team-a"`},
	}, clusterStorage.listOption.Filters)
}

func TestValidateEndpointAcceleratorVirtualizationMiddlewareSkipsCapacityLookupOnPatch(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		listError: errors.New("patch must not look up cluster capacity"),
	}
	body := `{
		"metadata": {"name": "endpoint", "workspace": "team-a"},
		"spec": {
			"cluster": "cluster-a",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "4096",
					"virtualization.core_percent": "50"
				}
			}
		}
	}`

	recorder := runEndpointAcceleratorVirtualizationValidation(http.MethodPatch, body, clusterStorage)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, 0, clusterStorage.listCalls)
}

func TestValidateEndpointAcceleratorVirtualizationMiddlewareRejectsUnsatisfiablePost(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 1024, 100),
	})
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
	}
	body := `{
		"metadata": {"name": "endpoint", "workspace": "team-a"},
		"spec": {
			"cluster": "cluster-a",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "Tesla-T4",
					"virtualization.memory_mib": "4096",
					"virtualization.core_percent": "50"
				}
			}
		}
	}`

	recorder, handlerCalled := runEndpointAcceleratorVirtualizationValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.False(t, handlerCalled)
}

func endpointWithAcceleratorVirtualization(cluster string, workspace string) *v1.Endpoint {
	return &v1.Endpoint{
		Metadata: &v1.Metadata{
			Name:      "endpoint",
			Workspace: workspace,
		},
		Spec: &v1.EndpointSpec{
			Cluster: cluster,
			Resources: acceleratorVirtualizationResources("1", "Tesla-T4", map[string]string{
				v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
				v1.AcceleratorVirtualizationCorePercentKey: "50",
			}),
		},
	}
}

func runEndpointAcceleratorVirtualizationValidation(method string, body string, clusterStorage storage.ClusterStorage) *httptest.ResponseRecorder {
	recorder, _ := runEndpointAcceleratorVirtualizationValidationWithHandler(method, body, clusterStorage)

	return recorder
}

func runEndpointAcceleratorVirtualizationValidationWithHandler(
	method string,
	body string,
	clusterStorage storage.ClusterStorage,
) (*httptest.ResponseRecorder, bool) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handlerCalled := false
	router.Handle(method, "/endpoints", validateEndpointAcceleratorVirtualization(clusterStorage), func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, "/endpoints", strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	return recorder, handlerCalled
}

func acceleratorVirtualizationResources(gpu string, product string, virtualization map[string]string) *v1.ResourceSpec {
	accelerator := map[string]string{
		v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
		v1.AcceleratorProductKey: product,
	}
	for key, value := range virtualization {
		accelerator[key] = value
	}

	return &v1.ResourceSpec{
		GPU:         &gpu,
		Accelerator: accelerator,
	}
}

func clusterWithNVIDIAGPUProduct(product string, productMemoryMiB float64, devices []*v1.DeviceResource) *v1.Cluster {
	return &v1.Cluster{
		Status: &v1.ClusterStatus{
			ResourceInfo: &v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							v1.AcceleratorTypeNVIDIAGPU: {
								Products: map[v1.AcceleratorProduct]*v1.AcceleratorProductResource{
									v1.AcceleratorProduct(product): {
										Quantity: 1,
										Virtualization: &v1.AcceleratorVirtualizationResource{
											MemoryMiB: productMemoryMiB,
											CoreUnits: 100,
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
							v1.AcceleratorProduct(product): {
								MemoryTotalMiB: productMemoryMiB,
							},
						},
					},
				},
				NodeResources: map[string]*v1.NodeResourceStatus{
					"node-0": {
						Devices: devices,
					},
				},
			},
		},
	}
}

func healthyDevice(uuid string, product string, memoryMiB int64, coreUnits int64) *v1.DeviceResource {
	return &v1.DeviceResource{
		UUID:    uuid,
		Product: product,
		Health:  true,
		Available: &v1.DeviceResourcePool{
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
		},
	}
}

func unhealthyDevice(uuid string, product string, memoryMiB int64, coreUnits int64) *v1.DeviceResource {
	device := healthyDevice(uuid, product, memoryMiB, coreUnits)
	device.Health = false

	return device
}

type fakeClusterStorage struct {
	clusters   []v1.Cluster
	listCalls  int
	listError  error
	listOption storage.ListOption
}

func (s *fakeClusterStorage) CreateCluster(data *v1.Cluster) error {
	return nil
}

func (s *fakeClusterStorage) DeleteCluster(id string) error {
	return nil
}

func (s *fakeClusterStorage) UpdateCluster(id string, data *v1.Cluster) error {
	return nil
}

func (s *fakeClusterStorage) GetCluster(id string) (*v1.Cluster, error) {
	return nil, nil
}

func (s *fakeClusterStorage) ListCluster(option storage.ListOption) ([]v1.Cluster, error) {
	s.listCalls++
	s.listOption = option

	if s.listError != nil {
		return nil, s.listError
	}

	return s.clusters, nil
}
