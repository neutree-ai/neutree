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

	t.Run("rejects unsupported memory percent", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryPercentKey: "50",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10219", err.Code)
		assert.Contains(t, err.Hint, "virtualization.memory_mib")
	})

	t.Run("rejects memory percent even when memory mib is set", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:     "8192",
			v1.AcceleratorVirtualizationMemoryPercentKey: "50",
			v1.AcceleratorVirtualizationCorePercentKey:   "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10219", err.Code)
	})

	t.Run("rejects missing vGPU memory mib", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.NotNil(t, err)
		assert.Equal(t, "10216", err.Code)
		assert.Contains(t, err.Hint, "virtualization.memory_mib")
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
		assert.Contains(t, err.Hint, "between 0 and 100")
	})

	t.Run("allows zero vGPU core resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "0",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.Nil(t, err)
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
				"variables": {"foo": "bar"}
			}
		}`))

		assert.Nil(t, err)
		assert.NotNil(t, endpoint)
		assert.Nil(t, validateEndpointVGPUPreflight(nil, http.MethodPatch, nil, endpoint))
	})
}

func TestMergeEndpointResourceSpec(t *testing.T) {
	t.Run("clears virtualization keys and preserves custom keys when switching to whole GPU", func(t *testing.T) {
		existing := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "4096",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
			"nvidia.com/gpucores":                      "50",
		})
		patch := &v1.ResourceSpec{
			Accelerator: map[string]string{
				v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
				v1.AcceleratorProductKey: "Tesla-T4",
			},
		}

		merged := mergeEndpointResourceSpec(existing, patch)

		assert.Equal(t, "50", merged.Accelerator["nvidia.com/gpucores"])
		assert.Empty(t, merged.Accelerator[v1.AcceleratorVirtualizationMemoryMiBKey])
		assert.Empty(t, merged.Accelerator[v1.AcceleratorVirtualizationCorePercentKey])
	})
}

func TestEndpointVGPUValidationAllowsPostWithoutCapacityPrecheck(t *testing.T) {
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsPostWhenProductMemorySpecIsMissing(t *testing.T) {
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
	assert.Equal(t, "10216", response.Code)
	assert.Contains(t, response.Hint, "physical GPU memory_mib")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsPostWhenMemoryMIBExceedsPhysicalCardSpec(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 2048, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 2048, 100),
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
	assert.Equal(t, "10216", response.Code)
	assert.Contains(t, response.Hint, "less than or equal to physical GPU memory_mib 2048")
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

func TestEndpointVGPUValidationAllowsMultiReplicaTotalDemandPost(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
	}
	body := `{
		"metadata": {"name": "endpoint", "workspace": "team-a"},
		"spec": {
			"cluster": "cluster-a",
			"replicas": {"num": 3},
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPausedPostWhenVirtualizationNotReady(t *testing.T) {
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
			"replicas": {"num": 0},
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
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

func TestEndpointVGPUValidationResolvesEndpointAndAllowsPatchWithoutCapacityPrecheck(t *testing.T) {
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsMergedPatchWhenOnlyGPUChanges(t *testing.T) {
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsMergedPatchWhenOnlyReplicasChange(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
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
			"replicas": {"num": 3}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPausePatchWhenVirtualizationNotReady(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		healthyDevice("gpu-0", "Tesla-T4", 8192, 100),
	})
	markClusterVGPUNotReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"replicas": {"num": 0}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPausePatchWithMissingCluster(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"cluster": "missing-cluster",
			"replicas": {"num": 0}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPausePatchWithInvalidVGPUResourceShape(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"replicas": {"num": 0},
			"resources": {
				"accelerator": {
					"virtualization.memory_mib": "4096",
					"virtualization.memory_percent": "50"
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
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsNegativeReplicaPatch(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"replicas": {"num": -1}
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
	assert.Equal(t, "10216", response.Code)
	assert.Contains(t, response.Hint, "spec.replicas.num")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsNonVGPUPatchWhenReplicasChange(t *testing.T) {
	gpu := "1"
	endpoint := endpointWithVGPU("cluster-a", "team-a")
	endpoint.Spec.Resources = &v1.ResourceSpec{
		GPU: &gpu,
		Accelerator: map[string]string{
			v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
			v1.AcceleratorProductKey: "Tesla-T4",
		},
	}
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{*endpoint},
	}
	body := `{
		"spec": {
			"replicas": {"num": 3}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithPath(
		http.MethodPatch,
		"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
		body,
		clusterStorage,
	)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPatchFromVGPUToWholeGPU(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
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
					"product": "Tesla-T4"
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
	assert.Equal(t, 0, clusterStorage.listCalls)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsMergedPatchWhenOnlyProductChangesToMissingMemorySpec(t *testing.T) {
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
	assert.Equal(t, "10216", response.Code)
	assert.Contains(t, response.Hint, "physical GPU memory_mib")
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPatchWhenCurrentAvailableCapacityIsZero(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		occupiedDevice("gpu-0", "Tesla-T4", 8192, 100),
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

func TestEndpointVGPUValidationAllowsWholeGPUToVGPUPatchWhenCurrentAvailableCapacityIsZero(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, []*v1.DeviceResource{
		occupiedDevice("gpu-0", "Tesla-T4", 16384, 100),
	})
	markClusterVGPUReady(cluster, "cluster-a", "team-a")

	gpu := "1"
	endpoint := endpointWithVGPU("cluster-a", "team-a")
	endpoint.Spec.Resources = &v1.ResourceSpec{
		GPU: &gpu,
		Accelerator: map[string]string{
			v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
			v1.AcceleratorProductKey: "Tesla-T4",
		},
	}
	endpoint.Status = &v1.EndpointStatus{
		Resources: &v1.EndpointResourceStatus{
			Replicas: []v1.ReplicaDeviceAllocation{
				{
					NodeID: "node-0",
					Devices: []v1.DeviceAllocation{
						{
							UUID:    "gpu-0",
							Product: "Tesla-T4",
							NodeID:  "node-0",
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
					"virtualization.memory_mib": "16384",
					"virtualization.core_percent": "100"
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
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPatchWhenTargetDeviceCannotPhysicallyFitVGPU(t *testing.T) {
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
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
					Allocatable: &v1.ResourceInfo{
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
					Allocatable: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{},
					},
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
		Allocatable: &v1.DeviceResourcePool{
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
		},
		Available: &v1.DeviceResourcePool{
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
		},
	}
}

func occupiedDevice(uuid string, product string, memoryMiB int64, coreUnits int64) *v1.DeviceResource {
	return &v1.DeviceResource{
		UUID:    uuid,
		Product: product,
		Health:  true,
		Allocatable: &v1.DeviceResourcePool{
			MemoryMiB: memoryMiB,
			CoreUnits: coreUnits,
		},
		Available: &v1.DeviceResourcePool{},
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
