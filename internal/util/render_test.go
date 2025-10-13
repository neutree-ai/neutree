package util

import (
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestRenderManifestWithMultipleObjects tests that renderManifest correctly handles
// YAML templates containing multiple objects separated by ---
func TestRenderManifestWithMultipleObjects(t *testing.T) {
	// Template with multiple objects
	multiObjectTemplate := `---
apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .EndpointName }}-config
  namespace: {{ .Namespace }}
data:
  key1: value1
---
apiVersion: v1
kind: Service
metadata:
  name: {{ .EndpointName }}-service
  namespace: {{ .Namespace }}
spec:
  type: ClusterIP
  ports:
  - port: 8000
---
apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
spec:
  replicas: {{ .Replicas }}`

	data := map[string]interface{}{
		"EndpointName": "multi-test",
		"Namespace":    "test-ns",
		"Replicas":     3,
	}

	objs, err := RenderKubernetesManifest(multiObjectTemplate, data)
	require.NoError(t, err, "Failed to render manifest with multiple objects")
	require.NotNil(t, objs, "Object list should not be nil")

	// Should have 3 objects
	assert.Equal(t, 3, len(objs.Items), "Should parse all 3 objects from the template")

	// Verify first object (ConfigMap)
	assert.Equal(t, "ConfigMap", objs.Items[0].GetKind(), "First object should be ConfigMap")
	assert.Equal(t, "multi-test-config", objs.Items[0].GetName(), "ConfigMap name mismatch")
	assert.Equal(t, "test-ns", objs.Items[0].GetNamespace(), "ConfigMap namespace mismatch")

	// Verify second object (Service)
	assert.Equal(t, "Service", objs.Items[1].GetKind(), "Second object should be Service")
	assert.Equal(t, "multi-test-service", objs.Items[1].GetName(), "Service name mismatch")
	assert.Equal(t, "test-ns", objs.Items[1].GetNamespace(), "Service namespace mismatch")

	// Verify third object (Deployment)
	assert.Equal(t, "Deployment", objs.Items[2].GetKind(), "Third object should be Deployment")
	assert.Equal(t, "multi-test", objs.Items[2].GetName(), "Deployment name mismatch")
	assert.Equal(t, "test-ns", objs.Items[2].GetNamespace(), "Deployment namespace mismatch")

	// Verify replicas in Deployment
	replicas, found, err := unstructured.NestedInt64(objs.Items[2].Object, "spec", "replicas")
	require.NoError(t, err, "Failed to get replicas from Deployment")
	require.True(t, found, "Replicas field should be found")
	assert.Equal(t, int64(3), replicas, "Deployment replicas mismatch")
}

// TestRenderManifestWithSingleObject tests backward compatibility with single object templates
func TestRenderManifestWithSingleObject(t *testing.T) {
	singleObjectTemplate := `apiVersion: apps/v1
kind: Deployment
metadata:
  name: {{ .EndpointName }}
  namespace: {{ .Namespace }}
spec:
  replicas: {{ .Replicas }}`

	data := map[string]interface{}{
		"EndpointName": "single-test",
		"Namespace":    "test-ns",
		"Replicas":     2,
	}

	objs, err := RenderKubernetesManifest(singleObjectTemplate, data)
	require.NoError(t, err, "Failed to render manifest with single object")
	require.NotNil(t, objs, "Object list should not be nil")

	// Should have 1 object
	assert.Equal(t, 1, len(objs.Items), "Should parse 1 object from the template")

	// Verify object
	assert.Equal(t, "Deployment", objs.Items[0].GetKind(), "Object should be Deployment")
	assert.Equal(t, "single-test", objs.Items[0].GetName(), "Deployment name mismatch")
	assert.Equal(t, "test-ns", objs.Items[0].GetNamespace(), "Deployment namespace mismatch")
}
