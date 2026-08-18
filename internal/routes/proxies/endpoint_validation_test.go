package proxies

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
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
		assert.Contains(t, err.Hint, "target accelerator product")
		assert.NotContains(t, err.Hint, "GPU")
	})

	t.Run("rejects vGPU endpoint without accelerator type", func(t *testing.T) {
		gpu := "1"
		resources := &v1.ResourceSpec{
			GPU: &gpu,
			Accelerator: map[string]string{
				v1.AcceleratorProductKey:                 "Tesla-T4",
				v1.AcceleratorVirtualizationMemoryMiBKey: "8192",
			},
		}

		err := validateEndpointVGPUResourceShape(resources)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10217", err.Code)
			assert.Equal(t, "endpoint accelerator virtualization requires accelerator type", err.Message)
			assert.Contains(t, err.Hint, "non-empty accelerator type")
			assert.NotContains(t, err.Hint, "NVIDIA")
		}
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
		assert.Contains(t, err.Hint, "between 1 and 100")
	})

	t.Run("rejects negative vGPU core resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "-1",
		})

		err := validateEndpointVGPUResourceShape(resources)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10216", err.Code)
			assert.Contains(t, err.Hint, "virtualization.core_percent")
			assert.Contains(t, err.Hint, "between 1 and 100")
		}
	})

	t.Run("rejects zero vGPU core resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
			v1.AcceleratorVirtualizationCorePercentKey: "0",
		})

		err := validateEndpointVGPUResourceShape(resources)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10216", err.Code)
			assert.Contains(t, err.Hint, "virtualization.core_percent")
			assert.Contains(t, err.Hint, "between 1 and 100")
		}
	})

	for _, corePercent := range []string{"1", "100"} {
		corePercent := corePercent
		t.Run("allows vGPU core resource "+corePercent, func(t *testing.T) {
			resources := vgpuResources("1", "Tesla-T4", map[string]string{
				v1.AcceleratorVirtualizationMemoryMiBKey:   "8192",
				v1.AcceleratorVirtualizationCorePercentKey: corePercent,
			})

			err := validateEndpointVGPUResourceShape(resources)

			assert.Nil(t, err)
		})
	}

	t.Run("allows omitted vGPU core resource", func(t *testing.T) {
		resources := vgpuResources("1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "8192",
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

	t.Run("allows a custom accelerator type", func(t *testing.T) {
		resources := virtualizationResources("custom_accelerator", "1", "example-product", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey:   "32768",
			v1.AcceleratorVirtualizationCorePercentKey: "50",
		})

		err := validateEndpointVGPUResourceShape(resources)

		assert.Nil(t, err)
	})
}

func TestValidateEndpointVGPUMemorySpec(t *testing.T) {
	t.Run("rejects a known maximum for the requested accelerator type", func(t *testing.T) {
		acceleratorType := v1.AcceleratorType("custom_accelerator")
		product := "example-product"
		cluster := clusterWithAcceleratorProduct(acceleratorType, product, 32768, nil)
		resources := virtualizationResources(string(acceleratorType), "1", product, map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "32769",
		})

		err := validateEndpointVGPUMemorySpec(resources, cluster)

		if assert.NotNil(t, err) {
			assert.Equal(t, "10216", err.Code)
			assert.Contains(t, err.Hint, "physical accelerator memory_mib 32768")
			assert.NotContains(t, err.Hint, "physical GPU")
		}
	})

	t.Run("does not use metadata from another accelerator type", func(t *testing.T) {
		product := "shared-product-name"
		cluster := clusterWithAcceleratorProduct(v1.AcceleratorTypeNVIDIAGPU, product, 1024, nil)
		resources := virtualizationResources("custom_accelerator", "1", product, map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "2048",
		})

		err := validateEndpointVGPUMemorySpec(resources, cluster)

		assert.Nil(t, err)
	})

	t.Run("allows an unknown physical memory maximum", func(t *testing.T) {
		resources := virtualizationResources("custom_accelerator", "1", "unknown-product", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "32768",
		})

		err := validateEndpointVGPUMemorySpec(resources, clusterWithoutNVIDIAGPUProducts())

		assert.Nil(t, err)
	})

	t.Run("allows a zero physical memory maximum", func(t *testing.T) {
		acceleratorType := v1.AcceleratorType("custom_accelerator")
		product := "example-product"
		cluster := clusterWithAcceleratorProduct(acceleratorType, product, 0, nil)
		resources := virtualizationResources(string(acceleratorType), "1", product, map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "32768",
		})

		err := validateEndpointVGPUMemorySpec(resources, cluster)

		assert.Nil(t, err)
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

func TestEndpointVGPUValidationAllowsCustomAcceleratorPost(t *testing.T) {
	acceleratorType := v1.AcceleratorType("custom_accelerator")
	product := "example-product"
	cluster := clusterWithAcceleratorProduct(acceleratorType, product, 65536, nil)
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
					"type": "custom_accelerator",
					"product": "example-product",
					"virtualization.memory_mib": "32768",
					"virtualization.core_percent": "50"
				}
			}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPostWhenProductMemorySpecIsMissing(t *testing.T) {
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

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsPostWithZeroCorePercent(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
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
					"virtualization.core_percent": "0"
				}
			}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsCorePercentUnderTemplateMode(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	cluster.Status.AcceleratorVirtualization = &v1.AcceleratorVirtualizationStatus{
		Mode: v1.AcceleratorVirtualizationModeTemplate,
		// template mode does not support compute-core shaping.
		SupportedResources: []string{v1.AcceleratorVirtualizationMemoryMiBKey},
	}
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
	assert.Equal(t, "10227", response.Code)
	assert.Contains(t, response.Message, "virtualization.core_percent")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationRejectsMemoryMiBWhenNotSupported(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	cluster.Status.AcceleratorVirtualization = &v1.AcceleratorVirtualizationStatus{
		Mode: v1.AcceleratorVirtualizationModeCore,
		// core mode that (for whatever reason) does not expose memory_mib
		// in its supported resources must still reject it.
		SupportedResources: []string{v1.AcceleratorVirtualizationCorePercentKey},
	}
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
	assert.Equal(t, "10227", response.Code)
	assert.Contains(t, response.Message, "virtualization.memory_mib")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsCorePercentUnderCoreMode(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	cluster.Status.AcceleratorVirtualization = &v1.AcceleratorVirtualizationStatus{
		Mode: v1.AcceleratorVirtualizationModeCore,
		// core mode supports both memory and compute-core shaping.
		SupportedResources: []string{
			v1.AcceleratorVirtualizationMemoryMiBKey,
			v1.AcceleratorVirtualizationCorePercentKey,
		},
	}
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

func TestEndpointVGPUValidationRejectsZeroCorePercentUnderTemplateMode(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	cluster.Status.AcceleratorVirtualization = &v1.AcceleratorVirtualizationStatus{
		Mode: v1.AcceleratorVirtualizationModeTemplate,
		// template mode does not support compute-core shaping.
		SupportedResources: []string{v1.AcceleratorVirtualizationMemoryMiBKey},
	}
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
					"virtualization.core_percent": "0"
				}
			}
		}
	}`

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, clusterStorage)

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10227", response.Code)
	assert.Contains(t, response.Message, "virtualization.core_percent")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsCorePercentWhenModeStatusMissing(t *testing.T) {
	// The capability block is absent (stale cluster written before this
	// feature); validation falls back to shape-only checks.
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
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
	assert.Contains(t, response.Hint, "less than or equal to physical accelerator memory_mib 2048")
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationReturnsInternalServerErrorOnClusterLookupError(t *testing.T) {
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
	assert.Equal(t, http.StatusInternalServerError, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "500", response.Code)
	assert.Equal(t, "internal server error", response.Message)
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
	assert.Equal(t, "invalid endpoint patch target", response.Message)
	assert.Contains(t, response.Hint, "endpoint lookup filters")
	assert.NotContains(t, response.Hint, "vGPU")
	assert.False(t, handlerCalled)
}

func TestEndpointValidationRejectsPatchWithInvalidTarget(t *testing.T) {
	tests := []struct {
		name               string
		path               string
		clusterStorage     *fakeClusterStorage
		expectedStatus     int
		expectedCode       string
		expectedMessage    string
		expectedHint       string
		expectedHintSuffix string
		expectedListCalls  int
	}{
		{
			name:              "without endpoint filters",
			path:              "/endpoints",
			clusterStorage:    &fakeClusterStorage{},
			expectedStatus:    http.StatusBadRequest,
			expectedHint:      "endpoint lookup filters",
			expectedListCalls: 0,
		},
		{
			name:              "when endpoint is not found",
			path:              "/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
			clusterStorage:    &fakeClusterStorage{},
			expectedStatus:    http.StatusBadRequest,
			expectedHint:      "endpoint not found",
			expectedListCalls: 1,
		},
		{
			name: "when endpoint lookup fails",
			path: "/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
			clusterStorage: &fakeClusterStorage{
				endpointListError: errors.New("database is down"),
			},
			expectedStatus:    http.StatusInternalServerError,
			expectedCode:      "500",
			expectedMessage:   "internal server error",
			expectedHint:      "",
			expectedListCalls: 1,
		},
		{
			name: "when multiple endpoints match",
			path: "/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
			clusterStorage: &fakeClusterStorage{
				endpoints: []v1.Endpoint{{}, {}},
			},
			expectedStatus:    http.StatusBadRequest,
			expectedHint:      "multiple endpoints matched",
			expectedListCalls: 1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			recorder, handlerCalled := runEndpointVGPUValidationWithPath(
				http.MethodPatch,
				tt.path,
				`{}`,
				tt.clusterStorage,
			)

			var response validationError
			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			expectedCode := tt.expectedCode
			if expectedCode == "" {
				expectedCode = "10221"
			}
			expectedMessage := tt.expectedMessage
			if expectedMessage == "" {
				expectedMessage = "invalid endpoint patch target"
			}
			assert.Equal(t, expectedCode, response.Code)
			assert.Equal(t, expectedMessage, response.Message)
			assert.Contains(t, response.Hint, tt.expectedHint)
			if tt.expectedHintSuffix != "" {
				assert.Contains(t, response.Hint, tt.expectedHintSuffix)
			}
			assert.NotContains(t, response.Hint, "vGPU")
			assert.False(t, handlerCalled)
			assert.Equal(t, tt.expectedListCalls, tt.clusterStorage.endpointListCalls)
		})
	}
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
	assert.Equal(t, 1, clusterStorage.endpointListCalls)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationReplacesResourcesWithoutInheritingVirtualization(t *testing.T) {
	existing := endpointWithVGPU("cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{*existing},
	}
	body := `{
		"spec": {
			"cluster": "cluster-a",
			"resources": {
				"gpu": "2",
				"accelerator": {
					"type": "custom_accelerator",
					"product": "replacement-product"
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
	// The accelerator resource shape validator performs one product-support
	// cluster lookup (fails open when no cluster is present).
	assert.LessOrEqual(t, clusterStorage.listCalls, 1)
	assert.Equal(t, "cluster-a", existing.Spec.Cluster)
	assert.Equal(t, "4096", existing.Spec.Resources.GetAcceleratorVirtualizationMemoryMiB())
	assert.Equal(t, "50", existing.Spec.Resources.GetAcceleratorVirtualizationCorePercent())
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsSpecReplacementWhenOnlyReplicasAreSupplied(t *testing.T) {
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
			"cluster": "cluster-a",
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
			"cluster": "cluster-a",
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

func TestEndpointVGPUValidationRejectsPausePatchThatChangesCluster(t *testing.T) {
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

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10225", response.Code)
	assert.False(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPausePatchWithInvalidVGPUResourceShape(t *testing.T) {
	clusterStorage := &fakeClusterStorage{
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
	}
	body := `{
		"spec": {
			"cluster": "cluster-a",
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
			"cluster": "cluster-a",
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
			"cluster": "cluster-a",
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
			"cluster": "cluster-a",
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
	// The accelerator resource shape validator performs one product-support
	// cluster lookup (fails open when no cluster is present).
	assert.LessOrEqual(t, clusterStorage.listCalls, 1)
	assert.True(t, handlerCalled)
}

func TestEndpointVGPUValidationAllowsPatchWhenReplacementProductMemoryIsUnknown(t *testing.T) {
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
			"cluster": "cluster-a",
			"resources": {
				"gpu": "1",
				"accelerator": {
					"type": "nvidia_gpu",
					"product": "L4",
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

func TestEndpointVGPUValidationRejectsPatchWithZeroCorePercent(t *testing.T) {
	cluster := clusterWithNVIDIAGPUProduct("Tesla-T4", 16384, nil)
	markClusterVGPUReady(cluster, "cluster-a", "team-a")
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
		endpoints: []v1.Endpoint{
			*endpointWithVGPU("cluster-a", "team-a"),
		},
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
					"virtualization.core_percent": "0"
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

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
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
			"cluster": "cluster-a",
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

	var response validationError
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10225", response.Code)
	assert.False(t, handlerCalled)
}

func TestEndpointValidationRejectsPatchThatChangesCluster(t *testing.T) {
	existing := &v1.Endpoint{
		Metadata: &v1.Metadata{Name: "endpoint", Workspace: "team-a"},
		Spec:     &v1.EndpointSpec{Cluster: "cluster-a"},
	}

	tests := []struct {
		name string
		body string
	}{
		{
			name: "different cluster",
			body: `{"spec":{"cluster":"cluster-b"}}`,
		},
		{
			name: "empty cluster",
			body: `{"spec":{"cluster":""}}`,
		},
		{
			name: "null cluster",
			body: `{"spec":{"cluster":null}}`,
		},
		{
			name: "null spec",
			body: `{"spec":null}`,
		},
		{
			name: "cluster change wins over invalid accelerator virtualization resources",
			body: `{
				"spec": {
					"cluster": "cluster-b",
					"resources": {
						"gpu": "1",
						"accelerator": {
							"type": "",
							"product": "Tesla-T4",
							"virtualization.memory_mib": "0",
							"virtualization.memory_percent": "50",
							"virtualization.core_percent": "0"
						}
					}
				}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterStorage := &fakeClusterStorage{endpoints: []v1.Endpoint{*existing}}

			recorder, handlerCalled := runEndpointVGPUValidationWithPath(
				http.MethodPatch,
				"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
				tt.body,
				clusterStorage,
			)

			var response validationError
			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, "10225", response.Code)
			assert.False(t, handlerCalled)
			assert.Equal(t, 1, clusterStorage.endpointListCalls)
			assert.Zero(t, clusterStorage.listCalls)
		})
	}
}

func TestBuildPostgrestEndpointPatchValidationNew(t *testing.T) {
	tests := []struct {
		name    string
		body    string
		wantErr bool
		assert  func(*testing.T, *v1.Endpoint, *v1.Endpoint)
	}{
		{
			name: "replaces a supplied spec composite without mutating Current",
			body: `{"spec":{"replicas":{"num":0}}}`,
			assert: func(t *testing.T, current, next *v1.Endpoint) {
				assert.NotSame(t, current, next)
				assert.Equal(t, "cluster-a", current.Spec.Cluster)
				assert.Equal(t, "1", *current.Spec.Resources.GPU)
				assert.Empty(t, next.Spec.Cluster)
				assert.Empty(t, next.Spec.Resources)
				assert.Equal(t, 0, *next.Spec.Replicas.Num)
			},
		},
		{
			name:    "rejects malformed patch payloads",
			body:    `[`,
			wantErr: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			gpu := "1"
			current := &v1.Endpoint{Spec: &v1.EndpointSpec{
				Cluster:   "cluster-a",
				Resources: &v1.ResourceSpec{GPU: &gpu},
			}}

			next, err := buildPostgrestEndpointPatchValidationNew(current, []byte(tt.body))
			if tt.wantErr {
				assert.Error(t, err)

				return
			}

			assert.NoError(t, err)
			tt.assert(t, current, next)
		})
	}
}

func TestEndpointVGPUValidationAllowsPatchWithoutClusterChange(t *testing.T) {
	existing := v1.Endpoint{
		Metadata: &v1.Metadata{Name: "endpoint", Workspace: "team-a"},
		Spec:     &v1.EndpointSpec{Cluster: "cluster-a"},
	}

	tests := []struct {
		name              string
		method            string
		body              string
		expectedStatus    int
		expectedCode      string
		expectedHandler   bool
		expectedListCalls int
	}{
		{
			name:              "same cluster patch",
			method:            http.MethodPatch,
			body:              `{"spec":{"cluster":"cluster-a"}}`,
			expectedStatus:    http.StatusNoContent,
			expectedHandler:   true,
			expectedListCalls: 1,
		},
		{
			name:              "patch without spec resolves current endpoint",
			method:            http.MethodPatch,
			body:              `{}`,
			expectedStatus:    http.StatusNoContent,
			expectedHandler:   true,
			expectedListCalls: 1,
		},
		{
			name:              "patch spec without cluster clears persisted cluster",
			method:            http.MethodPatch,
			body:              `{"spec":{"variables":{"foo":"bar"}}}`,
			expectedStatus:    http.StatusBadRequest,
			expectedCode:      "10225",
			expectedListCalls: 1,
		},
		{
			name:              "post remains allowed",
			method:            http.MethodPost,
			body:              `{"metadata":{"name":"new","workspace":"team-a"},"spec":{"cluster":"cluster-b"}}`,
			expectedStatus:    http.StatusNoContent,
			expectedHandler:   true,
			expectedListCalls: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			clusterStorage := &fakeClusterStorage{endpoints: []v1.Endpoint{existing}}

			recorder, handlerCalled := runEndpointVGPUValidationWithPath(
				tt.method,
				"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
				tt.body,
				clusterStorage,
			)

			assert.Equal(t, tt.expectedStatus, recorder.Code)
			assert.Equal(t, tt.expectedHandler, handlerCalled)
			assert.Equal(t, tt.expectedListCalls, clusterStorage.endpointListCalls)
			if tt.expectedCode != "" {
				var response validationError
				assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, tt.expectedCode, response.Code)
			}
		})
	}
}

func TestValidateEndpointSoftDelete(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name string
		body string
	}{
		{
			name: "skips ordinary patch validators",
			body: `{
				"metadata": {"deletion_timestamp": "2026-08-10T08:30:00Z"},
				"spec": {"cluster": "cluster-b"}
			}`,
		},
		{
			name: "bypasses malformed patch payload",
			body: `{
				"metadata": {"deletion_timestamp": "2026-08-10T08:30:00Z"},
				"spec": {"resources": []}
			}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			existing := v1.Endpoint{
				Metadata: &v1.Metadata{Name: "endpoint", Workspace: "team-a"},
				Spec:     &v1.EndpointSpec{Cluster: "cluster-a"},
			}
			clusterStorage := &fakeClusterStorage{endpoints: []v1.Endpoint{existing}}
			originalBody := &trackingReadCloser{Reader: strings.NewReader(tt.body)}
			handlerCalled := false
			router := gin.New()
			router.PATCH(
				"/endpoints",
				validateEndpoint(clusterStorage),
				func(c *gin.Context) {
					handlerCalled = true

					forwardedBody, err := io.ReadAll(c.Request.Body)
					assert.NoError(t, err)
					assert.Equal(t, tt.body, string(forwardedBody))
					assert.Equal(t, int64(len(tt.body)), c.Request.ContentLength)
					assert.Equal(t, strconv.Itoa(len(tt.body)), c.Request.Header.Get("Content-Length"))
					c.Status(http.StatusNoContent)
				},
			)

			req := httptest.NewRequest(
				http.MethodPatch,
				"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.team-a",
				nil,
			)
			req.Body = originalBody
			req.ContentLength = 0
			req.Header.Set("Content-Length", "0")
			recorder := httptest.NewRecorder()

			router.ServeHTTP(recorder, req)

			assert.True(t, handlerCalled)
			assert.True(t, originalBody.closed)
			assert.Equal(t, http.StatusNoContent, recorder.Code)
			assert.Zero(t, clusterStorage.endpointListCalls)
		})
	}
}

func TestEndpointValidationSkipsVGPUValidationForDeletedPost(t *testing.T) {
	body := `{
		"metadata": {"name": "endpoint", "workspace": "team-a", "deletion_timestamp": "2026-08-10T08:30:00Z"},
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

	recorder, handlerCalled := runEndpointVGPUValidationWithHandler(http.MethodPost, body, nil)

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
	router.Handle(
		method,
		"/endpoints",
		validateEndpoint(clusterStorage),
		func(c *gin.Context) {
			handlerCalled = true
			c.Status(http.StatusNoContent)
		},
	)

	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	return recorder, handlerCalled
}

func vgpuResources(gpu string, product string, virtualization map[string]string) *v1.ResourceSpec {
	return virtualizationResources(string(v1.AcceleratorTypeNVIDIAGPU), gpu, product, virtualization)
}

func virtualizationResources(acceleratorType string, gpu string, product string, virtualization map[string]string) *v1.ResourceSpec {
	accelerator := map[string]string{
		v1.AcceleratorTypeKey:    acceleratorType,
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
	return clusterWithAcceleratorProduct(v1.AcceleratorTypeNVIDIAGPU, product, productMemoryMiB, devices)
}

func clusterWithAcceleratorProduct(
	acceleratorType v1.AcceleratorType,
	product string,
	productMemoryMiB float64,
	devices []*v1.DeviceResource,
) *v1.Cluster {
	return &v1.Cluster{
		Status: &v1.ClusterStatus{
			ResourceInfo: &v1.ClusterResources{
				ResourceStatus: v1.ResourceStatus{
					Available: &v1.ResourceInfo{
						AcceleratorGroups: map[v1.AcceleratorType]*v1.AcceleratorGroup{
							acceleratorType: {
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
							acceleratorType: {
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
					acceleratorType: {
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

func TestValidateEndpointAcceleratorResourceShape(t *testing.T) {
	physicalResourceSpec := func(gpu string, accelerator map[string]string) *v1.Endpoint {
		return &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Cluster: "test-cluster",
				Resources: &v1.ResourceSpec{
					GPU:         &gpu,
					Accelerator: accelerator,
				},
			},
		}
	}

	physicalWithProduct := func(gpu, product string) *v1.Endpoint {
		return physicalResourceSpec(gpu, map[string]string{
			v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
			v1.AcceleratorProductKey: product,
		})
	}

	clusterWithType := func(clusterType string) *fakeClusterStorage {
		cluster := clusterWithAcceleratorProduct(v1.AcceleratorTypeNVIDIAGPU, "Tesla-T4", 16384, nil)
		cluster.Spec = &v1.ClusterSpec{Type: clusterType}

		return &fakeClusterStorage{clusters: []v1.Cluster{*cluster}}
	}

	k8sStore := clusterWithType(v1.KubernetesClusterType)
	sshStore := clusterWithType(v1.SSHClusterType)

	t.Run("allows a supported product with a positive integer count on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		assert.Nil(t, err)
	})

	t.Run("rejects an empty product on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1", "")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "product is required")
		}
	})

	t.Run("rejects an unknown product on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1", "unknown-model")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "unsupported accelerator product")
		}
	})

	t.Run("rejects a fractional count at or above one on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1.5", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("rejects a fractional count below one on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("0.5", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("rejects a zero count on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("0", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("rejects a negative count on a kubernetes cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("-1", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("rejects a zero count on a static cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("0", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "one-decimal value below 1")
		}
	})

	t.Run("rejects a missing count when an accelerator is declared", func(t *testing.T) {
		endpoint := physicalResourceSpec("", map[string]string{
			v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
			v1.AcceleratorProductKey: "Tesla-T4",
		})

		err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive accelerator card count")
		}
	})

	t.Run("allows a one-decimal count below one on a static cluster", func(t *testing.T) {
		for _, gpu := range []string{"0.1", "0.5", "0.9"} {
			endpoint := physicalWithProduct(gpu, "Tesla-T4")

			err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

			assert.Nil(t, err)
		}
	})

	t.Run("allows an integer count at or above one on a static cluster", func(t *testing.T) {
		for _, gpu := range []string{"1", "2", "8"} {
			endpoint := physicalWithProduct(gpu, "Tesla-T4")

			err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

			assert.Nil(t, err)
		}
	})

	t.Run("rejects a multi-decimal count below one on a static cluster", func(t *testing.T) {
		for _, gpu := range []string{"0.01", "0.15"} {
			endpoint := physicalWithProduct(gpu, "Tesla-T4")

			err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

			if assert.NotNil(t, err) {
				assert.Contains(t, err.Hint, "one-decimal value below 1")
			}
		}
	})

	t.Run("rejects a non-integer count at or above one on a static cluster", func(t *testing.T) {
		for _, gpu := range []string{"1.5", "2.5"} {
			endpoint := physicalWithProduct(gpu, "Tesla-T4")

			err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

			if assert.NotNil(t, err) {
				assert.Contains(t, err.Hint, "integer at or above 1")
			}
		}
	})

	t.Run("rejects a negative count on a static cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("-1", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "one-decimal value below 1")
		}
	})

	t.Run("rejects an empty product on a static cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1", "")

		err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "product is required")
		}
	})

	t.Run("rejects an unknown product on a static cluster", func(t *testing.T) {
		endpoint := physicalWithProduct("1", "unknown-model")

		err := validateEndpointAcceleratorResourceShape(sshStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "unsupported accelerator product")
		}
	})

	t.Run("rejects a malformed count", func(t *testing.T) {
		endpoint := physicalWithProduct("abc", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "valid accelerator card count")
		}
	})

	t.Run("rejects an infinite count", func(t *testing.T) {
		endpoint := physicalWithProduct("+Inf", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("rejects a NaN count", func(t *testing.T) {
		endpoint := physicalWithProduct("NaN", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Contains(t, err.Hint, "positive integer")
		}
	})

	t.Run("returns an internal error when the cluster lookup fails", func(t *testing.T) {
		errorStore := &fakeClusterStorage{listError: errors.New("database down")}
		endpoint := physicalWithProduct("1", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(errorStore, endpoint)

		if assert.NotNil(t, err) {
			assert.Equal(t, "500", err.Code)
			assert.Equal(t, http.StatusInternalServerError, err.HTTPStatus)
		}
	})

	t.Run("fails open when the cluster is not found", func(t *testing.T) {
		emptyStore := &fakeClusterStorage{}
		endpoint := physicalWithProduct("1.5", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(emptyStore, endpoint)

		assert.Nil(t, err)
	})

	t.Run("fails open when the cluster metadata is unavailable", func(t *testing.T) {
		cluster := clusterWithoutNVIDIAGPUProducts()
		cluster.Spec = &v1.ClusterSpec{Type: v1.KubernetesClusterType}
		noMetadataStore := &fakeClusterStorage{clusters: []v1.Cluster{*cluster}}
		endpoint := physicalWithProduct("1", "Tesla-T4")

		err := validateEndpointAcceleratorResourceShape(noMetadataStore, endpoint)

		assert.Nil(t, err)
	})

	t.Run("skips virtualization resources", func(t *testing.T) {
		resources := virtualizationResources(string(v1.AcceleratorTypeNVIDIAGPU), "1", "Tesla-T4", map[string]string{
			v1.AcceleratorVirtualizationMemoryMiBKey: "4096",
		})
		endpoint := &v1.Endpoint{
			Spec: &v1.EndpointSpec{
				Cluster:   "test-cluster",
				Resources: resources,
			},
		}

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		assert.Nil(t, err)
	})

	t.Run("skips when no accelerator type is declared", func(t *testing.T) {
		gpu := "1"
		endpoint := physicalResourceSpec(gpu, nil)

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		assert.Nil(t, err)
	})

	t.Run("skips when resources are nil", func(t *testing.T) {
		endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{Cluster: "test-cluster"}}

		err := validateEndpointAcceleratorResourceShape(k8sStore, endpoint)

		assert.Nil(t, err)
	})
}

func TestEndpointAcceleratorResourceShapeMiddleware(t *testing.T) {
	cluster := clusterWithAcceleratorProduct(v1.AcceleratorTypeNVIDIAGPU, "Tesla-T4", 16384, nil)
	cluster.Spec = &v1.ClusterSpec{Type: v1.KubernetesClusterType}
	clusterStorage := &fakeClusterStorage{
		clusters: []v1.Cluster{*cluster},
	}

	validBody := func(gpu, product string) string {
		return `{
			"metadata": {"name": "endpoint", "workspace": "default"},
			"spec": {
				"cluster": "test-cluster",
				"resources": {
					"gpu": "` + gpu + `",
					"accelerator": {"type": "nvidia_gpu", "product": "` + product + `"}
				}
			}
		}`
	}

	t.Run("rejects a fractional count on a kubernetes cluster create", func(t *testing.T) {
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("1.5", "Tesla-T4"), clusterStorage,
		)

		var response validationError
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "10230", response.Code)
		assert.Contains(t, response.Hint, "positive integer")
		assert.False(t, handlerCalled)
	})

	t.Run("rejects a zero count on a kubernetes cluster create", func(t *testing.T) {
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("0", "Tesla-T4"), clusterStorage,
		)

		var response validationError
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "10230", response.Code)
		assert.Contains(t, response.Hint, "positive integer")
		assert.False(t, handlerCalled)
	})

	t.Run("rejects an empty accelerator product on create", func(t *testing.T) {
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("1", ""), clusterStorage,
		)

		var response validationError
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "10230", response.Code)
		assert.Contains(t, response.Hint, "product is required")
		assert.False(t, handlerCalled)
	})

	t.Run("rejects an unknown accelerator product on create", func(t *testing.T) {
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("1", "unknown-model"), clusterStorage,
		)

		var response validationError
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "10230", response.Code)
		assert.Contains(t, response.Hint, "unsupported accelerator product")
		assert.False(t, handlerCalled)
	})

	t.Run("allows a supported product with a positive integer count on create", func(t *testing.T) {
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("1", "Tesla-T4"), clusterStorage,
		)

		assert.Equal(t, http.StatusNoContent, recorder.Code)
		assert.True(t, handlerCalled)
	})

	t.Run("fails open when the cluster metadata is unavailable", func(t *testing.T) {
		emptyStore := &fakeClusterStorage{}
		recorder, handlerCalled := runEndpointVGPUValidationWithHandler(
			http.MethodPost, validBody("1", "Tesla-T4"), emptyStore,
		)

		assert.Equal(t, http.StatusNoContent, recorder.Code)
		assert.True(t, handlerCalled)
	})

	t.Run("rejects a fractional count on a kubernetes cluster patch", func(t *testing.T) {
		existing := &v1.Endpoint{
			Metadata: &v1.Metadata{Name: "endpoint", Workspace: "default"},
			Spec: &v1.EndpointSpec{
				Cluster:   "test-cluster",
				Resources: physicalAcceleratorResources("1", "Tesla-T4"),
			},
		}
		patchStore := &fakeClusterStorage{
			clusters:  []v1.Cluster{*cluster},
			endpoints: []v1.Endpoint{*existing},
		}

		body := `{
			"spec": {
				"cluster": "test-cluster",
				"resources": {
					"gpu": "1.5",
					"accelerator": {"type": "nvidia_gpu", "product": "Tesla-T4"}
				}
			}
		}`

		recorder, handlerCalled := runEndpointVGPUValidationWithPath(
			http.MethodPatch,
			"/endpoints?metadata->>name=eq.endpoint&metadata->>workspace=eq.default",
			body,
			patchStore,
		)

		var response validationError
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
		assert.Equal(t, "10230", response.Code)
		assert.Contains(t, response.Hint, "positive integer")
		assert.False(t, handlerCalled)
	})
}

func TestEndpointPatchMayAffectResourceValidation(t *testing.T) {
	t.Run("runs when the patch touches resources", func(t *testing.T) {
		endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{Resources: &v1.ResourceSpec{}}}

		assert.True(t, endpointPatchMayAffectResourceValidation(endpoint))
	})

	t.Run("runs when the patch touches cluster", func(t *testing.T) {
		endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{Cluster: "cluster-a"}}

		assert.True(t, endpointPatchMayAffectResourceValidation(endpoint))
	})

	t.Run("runs for a replicas-only patch because it gates virtualization validation", func(t *testing.T) {
		replicas := 3
		endpoint := &v1.Endpoint{Spec: &v1.EndpointSpec{Replicas: v1.ReplicaSpec{Num: &replicas}}}

		assert.True(t, endpointPatchMayAffectResourceValidation(endpoint))
	})

	t.Run("skips for a nil spec", func(t *testing.T) {
		assert.False(t, endpointPatchMayAffectResourceValidation(nil))
		assert.False(t, endpointPatchMayAffectResourceValidation(&v1.Endpoint{}))
	})
}

func physicalAcceleratorResources(gpu string, product string) *v1.ResourceSpec {
	accelerator := map[string]string{
		v1.AcceleratorTypeKey:    string(v1.AcceleratorTypeNVIDIAGPU),
		v1.AcceleratorProductKey: product,
	}

	return &v1.ResourceSpec{
		GPU:         &gpu,
		Accelerator: accelerator,
	}
}
