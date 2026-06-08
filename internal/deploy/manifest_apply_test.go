package deploy

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func newFakeClient(objs ...client.Object) client.Client {
	scheme := runtime.NewScheme()
	return fake.NewClientBuilder().
		WithScheme(scheme).
		WithObjects(objs...).
		Build()
}

type fakeApplyClient struct {
	client.Client
}

func (c *fakeApplyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	if patch.Type() != types.ApplyPatchType {
		return c.Client.Patch(ctx, obj, patch, opts...)
	}

	current := obj.DeepCopyObject().(client.Object)
	err := c.Client.Get(ctx, client.ObjectKeyFromObject(obj), current)
	if apierrors.IsNotFound(err) {
		return c.Client.Create(ctx, obj)
	}
	if err != nil {
		return err
	}

	obj.SetResourceVersion(current.GetResourceVersion())
	return c.Client.Update(ctx, obj)
}

func createTestDeployment(name, namespace string, replicas int64) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "apps/v1",
			"kind":       "Deployment",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"replicas": replicas,
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Group:   "apps",
		Version: "v1",
		Kind:    "Deployment",
	})
	return obj
}

func createTestService(name, namespace string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"spec": map[string]interface{}{
				"ports": []interface{}{
					map[string]interface{}{
						"port": int64(80),
					},
				},
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Version: "v1",
		Kind:    "Service",
	})
	return obj
}

func createTestConfigMap(name, namespace, dataKey, dataValue string) *unstructured.Unstructured {
	obj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "ConfigMap",
			"metadata": map[string]interface{}{
				"name":      name,
				"namespace": namespace,
			},
			"data": map[string]interface{}{
				dataKey: dataValue,
			},
		},
	}
	obj.SetGroupVersionKind(schema.GroupVersionKind{
		Version: "v1",
		Kind:    "ConfigMap",
	})
	return obj
}

func TestNewManifestApply(t *testing.T) {
	fakeClient := newFakeClient()
	namespace := "test-namespace"

	ma := NewManifestApply(fakeClient, namespace)

	assert.NotNil(t, ma)
	assert.Equal(t, fakeClient, ma.ctrlClient)
	assert.Equal(t, namespace, ma.namespace)
	assert.Empty(t, ma.lastAppliedConfigJSON)
	assert.Nil(t, ma.newObjects)
	assert.Nil(t, ma.mutates)
}

func TestManifestApply_WithMethods(t *testing.T) {
	fakeClient := newFakeClient()
	ma := NewManifestApply(fakeClient, "test-ns")

	configJSON := `[{"apiVersion":"v1","kind":"Pod"}]`
	ma = ma.WithLastAppliedConfig(configJSON)
	assert.Equal(t, configJSON, ma.lastAppliedConfigJSON)

	objects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{
			*createTestDeployment("test", "test-ns", 1),
		},
	}
	ma = ma.WithNewObjects(objects)
	assert.Equal(t, objects, ma.newObjects)

	mutateFunc := func(obj *unstructured.Unstructured) error {
		return nil
	}
	ma = ma.WithMutate(mutateFunc)
	assert.NotNil(t, ma.mutates)

	logger := klog.NewKlogr()
	ma = ma.WithLogger(logger)
	assert.NotNil(t, ma.logger)
}

func TestComputeManifestDiff_NilNewObjects(t *testing.T) {
	fakeClient := newFakeClient()
	ma := NewManifestApply(fakeClient, "test-ns").
		WithLogger(klog.NewKlogr())

	_, err := ma.computeManifestDiff()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "newObjects is required")
}

func TestComputeManifestDiff_FirstDeployment(t *testing.T) {
	fakeClient := newFakeClient()

	deployment := createTestDeployment("nginx", "test-ns", 3)
	service := createTestService("nginx", "test-ns")

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*deployment, *service},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	diff, err := ma.computeManifestDiff()

	assert.NoError(t, err)
	assert.NotNil(t, diff)
	assert.True(t, diff.NeedsUpdate)
	assert.Len(t, diff.ChangedObjects, 2)
	assert.Len(t, diff.DeletedObjects, 0)
}

func TestComputeManifestDiff_NoChanges(t *testing.T) {
	fakeClient := newFakeClient()

	deployment := createTestDeployment("nginx", "test-ns", 3)

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*deployment},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	diff, err := ma.computeManifestDiff()

	assert.NoError(t, err)
	assert.NotNil(t, diff)
	assert.False(t, diff.NeedsUpdate)
	assert.Len(t, diff.ChangedObjects, 0)
	assert.Len(t, diff.DeletedObjects, 0)
}

func TestComputeManifestDiff_ObjectAdded(t *testing.T) {
	fakeClient := newFakeClient()

	deployment := createTestDeployment("nginx", "test-ns", 3)
	service := createTestService("nginx", "test-ns")

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*deployment, *service},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	diff, err := ma.computeManifestDiff()

	assert.NoError(t, err)
	assert.NotNil(t, diff)
	assert.True(t, diff.NeedsUpdate)
	assert.Len(t, diff.ChangedObjects, 1)
	assert.Equal(t, "Service", diff.ChangedObjects[0].GetKind())
	assert.Len(t, diff.DeletedObjects, 0)
}

func TestComputeManifestDiff_ObjectModified(t *testing.T) {
	fakeClient := newFakeClient()

	oldDeployment := createTestDeployment("nginx", "test-ns", 3)
	newDeployment := createTestDeployment("nginx", "test-ns", 5)

	lastApplied := []unstructured.Unstructured{*oldDeployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*newDeployment},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	diff, err := ma.computeManifestDiff()

	assert.NoError(t, err)
	assert.NotNil(t, diff)
	assert.True(t, diff.NeedsUpdate)
	assert.Len(t, diff.ChangedObjects, 1)
	assert.Equal(t, "Deployment", diff.ChangedObjects[0].GetKind())
	assert.Len(t, diff.DeletedObjects, 0)
}

func TestComputeManifestDiff_ObjectDeleted(t *testing.T) {
	fakeClient := newFakeClient()

	deployment := createTestDeployment("nginx", "test-ns", 3)
	service := createTestService("nginx", "test-ns")

	lastApplied := []unstructured.Unstructured{*deployment, *service}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*deployment},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	diff, err := ma.computeManifestDiff()

	assert.NoError(t, err)
	assert.NotNil(t, diff)
	assert.True(t, diff.NeedsUpdate)
	assert.Len(t, diff.ChangedObjects, 0)
	assert.Len(t, diff.DeletedObjects, 1)
	assert.Equal(t, "Service", diff.DeletedObjects[0].GetKind())
}

func TestComputeManifestDiff_InvalidLastAppliedJSON(t *testing.T) {
	fakeClient := newFakeClient()

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*createTestDeployment("nginx", "test-ns", 3)},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig("invalid json").
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	_, err := ma.computeManifestDiff()

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse last applied config")
}

func TestApplyManifests_NoChanges(t *testing.T) {
	deployment := createTestDeployment("nginx", "test-ns", 3)
	fakeClient := newFakeClient(deployment)

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*deployment},
	}

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	count, err := ma.ApplyManifests(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestApplyManifests_IgnoresLiveDeploymentDefaultedSpecFields(t *testing.T) {
	desiredDeployment := createTestDeployment("nginx", "test-ns", 1)
	liveDeployment := createTestDeployment("nginx", "test-ns", 1)
	assert.NoError(t, unstructured.SetNestedField(liveDeployment.Object, int64(10), "spec", "revisionHistoryLimit"))

	lastApplied := []unstructured.Unstructured{*desiredDeployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*desiredDeployment},
	}

	fakeClient := &fakeApplyClient{Client: newFakeClient(liveDeployment)}
	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	count, err := ma.ApplyManifests(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 0, count)
}

func TestApplyManifests_ReappliesWhenLiveDeploymentDriftsFromLastApplied(t *testing.T) {
	desiredDeployment := createTestDeployment("nginx", "test-ns", 1)
	liveDeployment := createTestDeployment("nginx", "test-ns", 0)

	lastApplied := []unstructured.Unstructured{*desiredDeployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*desiredDeployment},
	}

	fakeClient := &fakeApplyClient{Client: newFakeClient(liveDeployment)}
	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	count, err := ma.ApplyManifests(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 1, count)

	current := createTestDeployment("nginx", "test-ns", 0)
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: "test-ns",
		Name:      "nginx",
	}, current)
	assert.NoError(t, err)

	replicas, found, err := unstructured.NestedInt64(current.Object, "spec", "replicas")
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, int64(1), replicas)
}

func TestApplyManifests_RecreatesMissingLiveObjectWhenLastAppliedMatchesDesired(t *testing.T) {
	desiredService := createTestService("nginx", "test-ns")

	lastApplied := []unstructured.Unstructured{*desiredService}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*desiredService},
	}

	fakeClient := &fakeApplyClient{Client: newFakeClient()}
	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	count, err := ma.ApplyManifests(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 1, count)

	current := createTestService("nginx", "test-ns")
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: "test-ns",
		Name:      "nginx",
	}, current)
	assert.NoError(t, err)
	assert.Equal(t, "Service", current.GetKind())
}

func TestApplyManifests_ReappliesWhenLiveNoSpecObjectDriftsFromLastApplied(t *testing.T) {
	desiredConfigMap := createTestConfigMap("settings", "test-ns", "mode", "expected")
	liveConfigMap := createTestConfigMap("settings", "test-ns", "mode", "stale")

	lastApplied := []unstructured.Unstructured{*desiredConfigMap}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	newObjects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{*desiredConfigMap},
	}

	fakeClient := &fakeApplyClient{Client: newFakeClient(liveConfigMap)}
	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithNewObjects(newObjects).
		WithLogger(klog.NewKlogr())

	count, err := ma.ApplyManifests(context.Background())

	assert.NoError(t, err)
	assert.Equal(t, 1, count)

	current := createTestConfigMap("settings", "test-ns", "mode", "stale")
	err = fakeClient.Get(context.Background(), client.ObjectKey{
		Namespace: "test-ns",
		Name:      "settings",
	}, current)
	assert.NoError(t, err)

	mode, found, err := unstructured.NestedString(current.Object, "data", "mode")
	assert.NoError(t, err)
	assert.True(t, found)
	assert.Equal(t, "expected", mode)
}

func TestDelete_NoLastAppliedConfig(t *testing.T) {
	fakeClient := newFakeClient()

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLogger(klog.NewKlogr())

	finished, err := ma.Delete(context.Background())

	assert.NoError(t, err)
	assert.True(t, finished)
}

func TestDelete_InvalidLastAppliedJSON(t *testing.T) {
	fakeClient := newFakeClient()

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig("invalid json").
		WithLogger(klog.NewKlogr())

	_, err := ma.Delete(context.Background())

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "failed to parse last applied config")
}

func TestDelete_ResourceNotFound(t *testing.T) {
	deployment := createTestDeployment("nginx", "test-ns", 3)

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	fakeClient := newFakeClient()

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithLogger(klog.NewKlogr())

	finished, err := ma.Delete(context.Background())

	assert.NoError(t, err)
	assert.True(t, finished)
}

func TestDelete_ResourceExists(t *testing.T) {
	deployment := createTestDeployment("nginx", "test-ns", 3)

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	fakeClient := newFakeClient(deployment)

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithLogger(klog.NewKlogr())

	finished, err := ma.Delete(context.Background())

	assert.NoError(t, err)
	assert.False(t, finished)
}

func TestDelete_AlreadyMarkedForDeletion(t *testing.T) {
	deployment := createTestDeployment("nginx", "test-ns", 3)
	now := metav1.Now()
	deployment.SetDeletionTimestamp(&now)
	// Add finalizer so fake client accepts the object with deletionTimestamp
	deployment.SetFinalizers([]string{"test-finalizer"})

	lastApplied := []unstructured.Unstructured{*deployment}
	lastAppliedJSON, _ := json.Marshal(lastApplied)

	fakeClient := newFakeClient(deployment)

	ma := NewManifestApply(fakeClient, "test-ns").
		WithLastAppliedConfig(string(lastAppliedJSON)).
		WithLogger(klog.NewKlogr())

	finished, err := ma.Delete(context.Background())

	assert.NoError(t, err)
	assert.False(t, finished)
}

func TestObjectKey(t *testing.T) {
	tests := []struct {
		name     string
		obj      *unstructured.Unstructured
		expected string
	}{
		{
			name:     "deployment",
			obj:      createTestDeployment("nginx", "test-ns", 3),
			expected: "apps/v1/Deployment/test-ns/nginx",
		},
		{
			name:     "service",
			obj:      createTestService("nginx", "test-ns"),
			expected: "v1/Service/test-ns/nginx",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			key := objectKey(tt.obj)
			assert.Equal(t, tt.expected, key)
		})
	}
}

func TestComputeSpecHash(t *testing.T) {
	tests := []struct {
		name     string
		obj1     *unstructured.Unstructured
		obj2     *unstructured.Unstructured
		shouldEq bool
	}{
		{
			name:     "same object same hash",
			obj1:     createTestDeployment("nginx", "test-ns", 3),
			obj2:     createTestDeployment("nginx", "test-ns", 3),
			shouldEq: true,
		},
		{
			name:     "different replicas different hash",
			obj1:     createTestDeployment("nginx", "test-ns", 3),
			obj2:     createTestDeployment("nginx", "test-ns", 5),
			shouldEq: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			hash1 := computeSpecHash(tt.obj1)
			hash2 := computeSpecHash(tt.obj2)

			assert.NotEmpty(t, hash1)
			assert.NotEmpty(t, hash2)

			if tt.shouldEq {
				assert.Equal(t, hash1, hash2)
			} else {
				assert.NotEqual(t, hash1, hash2)
			}
		})
	}
}
