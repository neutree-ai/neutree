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
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
	"github.com/stretchr/testify/assert"
)

func TestValidateEndpointVGPUResourceShape(t *testing.T) {
	t.Run("allows non vGPU endpoint resources", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", nil)

		err := validateEndpointVGPUResourceShape(resources)

		assert.Nil(t, err)
	})

	t.Run("allows raw accelerator keys without virtualization fields", func(t *testing.T) {
		gpu := "1"
		resources := &v1.ResourceSpec{
			GPU: &gpu,
			Accelerator: map[string]string{
				v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
				v1.AcceleratorProductKey: "Tesla-T4",
				"nvidia.com/gpucores":    "50",
			},
		}

		err := validateEndpointVGPUResourceShape(resources)

		assert.Nil(t, err)
	})

	t.Run("rejects vGPU endpoint without product", func(t *testing.T) {
		resources := vgpuResources("1", "", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "8192",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10218", err.Code)
	})

	t.Run("rejects mutually exclusive memory fields", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:     "8192",
			v1.AcceleratorVirtualizationMemoryPercentKey: "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10219", err.Code)
	})

	t.Run("rejects invalid vGPU numeric resources", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "101",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
	})

	t.Run("rejects fractional vGPU memory resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192.5",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.memory_mib")
		assert.Contains(t, err.Hint, "positive integer")
	})

	t.Run("rejects fractional vGPU core resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "50.5",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.core_percent")
		assert.Contains(t, err.Hint, "positive integer")
	})

	t.Run("allows vGPU endpoint resource shape without cluster availability lookup", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.Nil(t, err)
	})

	t.Run("skips patch that does not touch resources", func(t *testing.T) {
		endpoint, err := parseEndpointBody([]byte(`{
			"spec": {
				"replicas": {"num": 2}
			}
		}`))

		assert.Nil(t, err)
		assert.NotNil(t, endpoint)
		assert.Nil(t, validateEndpointVGPUPreflight(nil, http.MethodPatch, nil, endpoint))
	})
}

func TestValidateEndpointVGPUCapacity(t *testing.T) {
	t.Run("skips when cluster resource info is unavailable", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := &v1.Cluster{
			Status: &v1.ClusterStatus{},
		}

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("skips when available accelerator telemetry is incomplete", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := &v1.Cluster{
			Status: &v1.ClusterStatus{
				ResourceInfo: &v1.ClusterResources{},
			},
		}

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects request that exceeds per device memory availability", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8193",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "Tesla-T4")
		assert.Contains(t, err.Hint, "satisfiable")
	})

	t.Run("rejects product absent from cluster resource info", func(t *testing.T) {
		resources := vgpuResources("1", "Missing-GPU", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "Missing-GPU")
	})

	t.Run("skips when product exists but virtualization telemetry is missing", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})
		cluster.Status.ResourceInfo.Available.AcceleratorGroups[v1.AcceleratorTypeNVIDIAGPU].
			Products[v1.AcceleratorProduct("Tesla-T4")].Virtualization = nil

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects fragmented capacity that cannot satisfy each requested virtual card", func(t *testing.T) {
		resources := vgpuResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8193",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 32768, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
			healthyDevice("gpu-1", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_gpu=2")
	})

	t.Run("rejects request that exceeds per device core availability", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "51",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 50),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10220", err.Code)
			assert.Contains(t, err.Hint, "requested_core_units=51")
			assert.Contains(t, err.Hint, "satisfiable_devices=0")
		}
	})

	t.Run("rejects request that needs more healthy matching devices than available", func(t *testing.T) {
		resources := vgpuResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_gpu=2")
		assert.Contains(t, err.Hint, "satisfiable_devices=1")
	})

	t.Run("ignores unhealthy matching devices", func(t *testing.T) {
		resources := vgpuResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 32768, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
			unhealthyDevice("gpu-1", "Tesla-T4", 8192, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.NotNil(t, err)
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "satisfiable_devices=1")
	})

	t.Run("allows request when enough healthy matching devices fit", func(t *testing.T) {
		resources := vgpuResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 8192, 50),
			healthyDevice("gpu-1", "Tesla-T4", 8192, 50),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("derives memory from memory percent using product metadata", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "51",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 10001, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 5101, 100),
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("skips memory percent precheck when product memory metadata is missing", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "51",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 10001, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 5101, 100),
		})
		cluster.Status.ResourceInfo.AcceleratorMetadata = nil

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects core overuse when memory percent metadata is missing", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "51",
			v1.AcceleratorVirtualizationCorePercentKey:   "51",
		})
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 10001, []*v1.DeviceResource{
			healthyDevice("gpu-0", "Tesla-T4", 5101, 50),
		})
		cluster.Status.ResourceInfo.AcceleratorMetadata = nil

		err := validateEndpointVGPUCapacity(resources, cluster)

		if err == nil {
			t.Fatal("expected capacity error")
		}
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_core_units=51")
		assert.Contains(t, err.Hint, "satisfiable_devices=0")
	})

	t.Run("skips when matching device availability telemetry is incomplete", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		device := healthyDevice("gpu-0", "Tesla-T4", 8192, 100)
		device.Available = nil
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			device,
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("rejects when matching device count is insufficient despite incomplete availability telemetry", func(t *testing.T) {
		resources := vgpuResources("2", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})
		device := healthyDevice("gpu-0", "Tesla-T4", 8192, 100)
		device.Available = nil
		cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
			device,
		})

		err := validateEndpointVGPUCapacity(resources, cluster)

		if err == nil {
			t.Fatal("expected capacity error")
		}
		assert.Equal(t, "10220", err.Code)
		assert.Contains(t, err.Hint, "requested_gpu=2")
		assert.Contains(t, err.Hint, "matching_devices=1")
	})
}

func TestEndpointVGPUValidationRejectsUnsatisfiablePost(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 1024, 100),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsNoGPUClusterPost(t *testing.T) {
	cluster := clusterWithoutNVIDIAGPUProducts()
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.Contains(t, response.Hint, "product=Tesla-T4 has no available")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationReturnsServiceUnavailableOnClusterLookupError(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		listError: errors.New("database is down"),
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10221", response.Code)
	assert.Contains(t, response.Hint, "failed to look up cluster")
	assert.NotContains(t, response.Hint, "database is down")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsNotReadyPost(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
	})
	markClusterVGPUNotReady(cluster, "cluster-a", "team-a")
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10222", response.Code)
	assert.Contains(t, response.Hint, "not ready")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsPatchWithoutEndpointFilters(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 1024, 100),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPatch, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10221", response.Code)
	assert.Contains(t, response.Hint, "endpoint lookup filters")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationResolvesEndpointAndRejectsUnsatisfiablePatch(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 1024, 100),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
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

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsMergedPatchWhenOnlyGPUChanges(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 4096, 50),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"resources": {
				"gpu": "2"
			}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsMergedPatchWhenOnlyProductChanges(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 4096, 50),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"resources": {
				"accelerator": {
					"product": "L4"
				}
			}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAddsBackCurrentPatchAllocation(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 0, 0),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	endpoint := endpointWithVGPU("cluster-a", "team-a")
	endpoint.Status = &v1.EndpointStatus{
		Resources: &v1.EndpointResourceStatus{
			Replicas: []v1.ReplicaDeviceAllocation{
				{
					NodeID: "node-0",
					Devices: []v1.DeviceAllocation{
						{
							UUID:      "gpu-0",
							Product:   "Tesla-T4",
							MemoryMiB: 4096,
							CoreUnits: 50,
							NodeID:    "node-0",
						},
					},
				},
			},
		},
	}
	clusterStorage := &fakeClusterStorage{
		clusters:  []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{*endpoint},
	}
	body := `{
		"spec": {
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

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationDoesNotAddBackAllocationWhenPatchMovesCluster(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 0, 0),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	endpoint := endpointWithVGPU("old-cluster", "team-a")
	endpoint.Status = &v1.EndpointStatus{
		Resources: &v1.EndpointResourceStatus{
			Replicas: []v1.ReplicaDeviceAllocation{
				{
					NodeID: "node-0",
					Devices: []v1.DeviceAllocation{
						{
							UUID:      "gpu-0",
							Product:   "Tesla-T4",
							MemoryMiB: 4096,
							CoreUnits: 50,
							NodeID:    "node-0",
						},
					},
				},
			},
		},
	}
	clusterStorage := &fakeClusterStorage{
		clusters:  []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{*endpoint},
	}
	body := `{
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

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10220", response.Code)
	assert.False(t, handlerCalled)
}

func endpointWithVGPU(cluster string, workspace string) *v1.Endpoint {
	return &v1.Endpoint{
		Metadata: &v1.Metadata{
			Name:      "endpoint",
			Workspace: workspace,
		},
		Spec: &v1.EndpointSpec{
			Cluster: cluster,
			Resources: vgpuResources("1", "Tesla-T4", map[string]string{
				v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
				v1.AcceleratorVirtualizationCorePercentKey: "50",
			}),
		},
	}
}

func runEndpointVGPUValidation(method string, body string, clusterStorage storage.Storage) *httptest.ResponseRecorder {
	recorder, _ := runEndpointVGPUValidationWithHandler(method, body, clusterStorage)

	return recorder
}

func runEndpointVGPUValidationWithPath(
	method string,
	path string,
	body string,
	clusterStorage storage.Storage,
) (*httptest.ResponseRecorder, bool) {
	return runEndpointVGPUValidationWithHandlerAndPath(method, path, body, clusterStorage)
}

func runEndpointVGPUValidationWithHandler(
	method string,
	body string,
	clusterStorage storage.Storage,
) (*httptest.ResponseRecorder, bool) {
	return runEndpointVGPUValidationWithHandlerAndPath(method, "/endpoints", body, clusterStorage)
}

func runEndpointVGPUValidationWithHandlerAndPath(
	method string,
	path string,
	body string,
	clusterStorage storage.Storage,
) (*httptest.ResponseRecorder, bool) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	handlerCalled := false
	router.Handle(method, "/endpoints", validateEndpointVGPU(clusterStorage), func(c *gin.Context) {
		handlerCalled = true
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	return recorder, handlerCalled
}

func vgpuResources(gpu string, product string, virtualization map[string]string) *v1.ResourceSpec {
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

func clusterWithoutNVIDIAGPUProducts() *v1.Cluster {
	return &v1.Cluster{
		Status: &v1.ClusterStatus{
			ResourceInfo: &v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{},
					},
				},
				NodeResources: map[string]*v1.NodeResourceStatus{},
			},
		},
	}
}

func markClusterVGPUReady(cluster *v1.Cluster, name string, workspace string) {
	if cluster.Metadata == nil {
		cluster.Metadata = &v1.Metadata{}
	}
	cluster.Metadata.Name = name
	cluster.Metadata.Workspace = workspace
	cluster.Spec = &v1.ClusterSpec{
		Type: v1.KubernetesClusterType,
		AcceleratorVirtualization: &v1.AcceleratorVirtualizationSpec{
			Enabled: true,
		},
	}
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}
	cluster.Status.ComponentStatus = map[string]*v1.ComponentStatus{
		v1.ComponentStatusAcceleratorVirtualizationKey: {
			Phase: v1.ComponentPhaseReady,
		},
	}
}

func markClusterVGPUNotReady(cluster *v1.Cluster, name string, workspace string) {
	markClusterVGPUReady(cluster, name, workspace)
	cluster.Status.ComponentStatus[v1.ComponentStatusAcceleratorVirtualizationKey] = &v1.ComponentStatus{
		Phase:   v1.ComponentPhaseNotReady,
		Reason:  "HAMiNotReady",
		Message: "HAMi device plugin is not ready",
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
	*storagemocks.MockStorage

	clusters           []v1.Cluster
	endpoints          []v1.Endpoint
	listCalls          int
	endpointListCalls  int
	listError          error
	endpointListError  error
	listOption         storage.ListOption
	endpointListOption storage.ListOption
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

func (s *fakeClusterStorage) CreateEndpoint(data *v1.Endpoint) error {
	return nil
}

func (s *fakeClusterStorage) DeleteEndpoint(id string) error {
	return nil
}

func (s *fakeClusterStorage) UpdateEndpoint(id string, data *v1.Endpoint) error {
	return nil
}

func (s *fakeClusterStorage) GetEndpoint(id string) (*v1.Endpoint, error) {
	return nil, nil
}

func (s *fakeClusterStorage) ListEndpoint(option storage.ListOption) ([]v1.Endpoint, error) {
	s.endpointListCalls++
	s.endpointListOption = option

	if s.endpointListError != nil {
		return nil, s.endpointListError
	}

	return s.endpoints, nil
}
