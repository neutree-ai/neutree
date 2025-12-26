package deploy

import (
	"context"
	"encoding/json"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestKubernetesDeployer_Apply(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name          string
		resourceName  string
		componentName string
		objects       *unstructured.UnstructuredList
		labels        map[string]string
		wantError     bool
	}{
		{
			name:          "apply without objects",
			resourceName:  "test-cluster",
			componentName: "router",
			objects:       nil,
			wantError:     true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			ctx := context.Background()

			applier := NewKubernetesDeployer(fakeClient, "default", tt.resourceName, tt.componentName)

			if tt.objects != nil {
				applier.WithNewObjects(tt.objects)
			}
			if tt.labels != nil {
				applier.WithLabels(tt.labels)
			}
			applier.WithLogger(klog.Background())

			_, err := applier.Apply(ctx)

			if tt.wantError {
				if err == nil {
					t.Errorf("Apply() error = nil, want error")
				}
				return
			}

			if err != nil {
				t.Fatalf("Apply() error = %v", err)
			}
		})
	}
}

func TestKubernetesDeployer_Delete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()

	resourceName := "test-cluster"
	componentName := "router"

	applier := NewKubernetesDeployer(fakeClient, "default", resourceName, componentName).
		WithLogger(klog.Background())

	// Test Delete without applying first (should not error)
	deleteFinished, err := applier.Delete(ctx)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	if !deleteFinished {
		t.Errorf("Delete() finished = %v, want true", deleteFinished)
	}
}

func TestKubernetesDeployer_WithMethods(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	objects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{
			{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "test",
						"namespace": "default",
					},
				},
			},
		},
	}

	labels := map[string]string{
		"app": "test",
	}

	logger := klog.Background()

	applier := NewKubernetesDeployer(fakeClient, "default", "resource", "component").
		WithNewObjects(objects).
		WithLabels(labels).
		WithLogger(logger)

	// Verify chain returns self
	if applier.newObjects != objects {
		t.Errorf("WithNewObjects() did not set objects")
	}
	if applier.labels["app"] != "test" {
		t.Errorf("WithLabels() did not set labels")
	}
	if applier.logger.GetSink() != logger.GetSink() {
		t.Errorf("WithLogger() did not set logger")
	}
}

func TestKubernetesDeployer_WithMutate(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	mutateCalled := false
	mutateFunc := func(obj *unstructured.Unstructured) error {
		mutateCalled = true
		// Add a custom annotation
		annotations := obj.GetAnnotations()
		if annotations == nil {
			annotations = make(map[string]string)
		}
		annotations["custom"] = "value"
		obj.SetAnnotations(annotations)
		return nil
	}

	applier := NewKubernetesDeployer(fakeClient, "default", "resource", "component").
		WithMutate(mutateFunc)

	if applier.mutates == nil {
		t.Error("WithMutate() did not set mutates function")
	}

	// Test the mutate function
	testObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name": "test-service",
			},
		},
	}

	err := applier.mutates[0](testObj)
	if err != nil {
		t.Fatalf("mutate() error = %v", err)
	}

	if !mutateCalled {
		t.Error("mutate function was not called")
	}

	annotations := testObj.GetAnnotations()
	if annotations["custom"] != "value" {
		t.Errorf("mutate function did not add annotation, got %v", annotations)
	}
}

func TestKubernetesDeployer_LabelsAppliedToObjects(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	objects := &unstructured.UnstructuredList{
		Items: []unstructured.Unstructured{
			{
				Object: map[string]interface{}{
					"apiVersion": "v1",
					"kind":       "ConfigMap",
					"metadata": map[string]interface{}{
						"name":      "test-config",
						"namespace": "default",
					},
					"data": map[string]interface{}{
						"key": "value",
					},
				},
			},
		},
	}

	labels := map[string]string{
		"app":       "test-app",
		"component": "router",
	}

	applier := NewKubernetesDeployer(fakeClient, "default", "test-cluster", "router").
		WithNewObjects(objects).
		WithLabels(labels).
		WithLogger(klog.Background())

	// Verify mutate function was created
	if applier.mutates == nil {
		t.Error("WithLabels() did not create mutate function")
	}

	// Test mutate function
	testObj := &unstructured.Unstructured{
		Object: map[string]interface{}{
			"apiVersion": "v1",
			"kind":       "Service",
			"metadata": map[string]interface{}{
				"name": "test-service",
			},
		},
	}

	err := applier.mutates[0](testObj)
	if err != nil {
		t.Fatalf("mutate() error = %v", err)
	}

	objLabels := testObj.GetLabels()
	for k, v := range labels {
		if objLabels[k] != v {
			t.Errorf("Object missing label %s=%s, got %v", k, v, objLabels)
		}
	}
}

func TestKubernetesDeployer_ConfigSaved(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	ctx := context.Background()

	resourceName := "test-cluster"
	componentName := "router"

	// Directly save a config using ConfigStore
	store := NewConfigStore(fakeClient)
	testConfig := `[{"apiVersion":"v1","kind":"ConfigMap","metadata":{"name":"test-config","namespace":"default"}}]`
	err := store.Set(ctx, "default", resourceName, componentName, testConfig, nil)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Verify config was saved
	savedConfig, err := store.Get(ctx, "default", resourceName, componentName)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}

	if savedConfig != testConfig {
		t.Errorf("Config not saved correctly, got %v, want %v", savedConfig, testConfig)
	}

	// Verify saved config is valid JSON
	var items []map[string]interface{}
	err = json.Unmarshal([]byte(savedConfig), &items)
	if err != nil {
		t.Errorf("Saved config is not valid JSON: %v", err)
	}

	if len(items) != 1 {
		t.Errorf("Saved config has %d items, want 1", len(items))
	}
}
