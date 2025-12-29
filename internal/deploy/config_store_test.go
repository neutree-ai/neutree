package deploy

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConfigStore_GetSet(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	tests := []struct {
		name          string
		resourceName  string
		componentName string
		config        string
		labels        map[string]string
	}{
		{
			name:          "basic config",
			resourceName:  "test-cluster",
			componentName: "router",
			config:        `{"test": "config"}`,
			labels: map[string]string{
				"cluster":   "test-cluster",
				"workspace": "default",
			},
		},
		{
			name:          "empty config",
			resourceName:  "test-endpoint",
			componentName: "deployment",
			config:        "",
			labels:        nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			store := NewConfigStore(fakeClient)
			ctx := context.Background()

			// Test Get on non-existent ConfigMap
			config, err := store.Get(ctx, "default", tt.resourceName, tt.componentName)
			if err != nil {
				t.Fatalf("Get() error = %v", err)
			}
			if config != "" {
				t.Errorf("Get() = %v, want empty string", config)
			}

			// Test Set
			err = store.Set(ctx, "default", tt.resourceName, tt.componentName, tt.config, tt.labels)
			if err != nil {
				t.Fatalf("Set() error = %v", err)
			}

			// Test Get after Set
			config, err = store.Get(ctx, "default", tt.resourceName, tt.componentName)
			if err != nil {
				t.Fatalf("Get() after Set error = %v", err)
			}
			if config != tt.config {
				t.Errorf("Get() = %v, want %v", config, tt.config)
			}

			// Verify ConfigMap exists with correct labels
			cm := &corev1.ConfigMap{}
			cmName := store.buildConfigMapName(tt.resourceName, tt.componentName)
			err = fakeClient.Get(ctx, client.ObjectKey{
				Namespace: "default",
				Name:      cmName,
			}, cm)
			if err != nil {
				t.Fatalf("ConfigMap not found: %v", err)
			}

			if cm.Labels[ManagedByLabel] != ManagedByValue {
				t.Errorf("ConfigMap missing managed-by label")
			}
			if cm.Labels["neutree.io/resource"] != tt.resourceName {
				t.Errorf("ConfigMap resource label = %v, want %v", cm.Labels["neutree.io/resource"], tt.resourceName)
			}
		})
	}
}

func TestConfigStore_Update(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := NewConfigStore(fakeClient)
	ctx := context.Background()

	resourceName := "test-cluster"
	componentName := "router"

	// Create initial config
	initialConfig := `{"version": "1"}`
	err := store.Set(ctx, "default", resourceName, componentName, initialConfig, nil)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Update config
	updatedConfig := `{"version": "2"}`
	err = store.Set(ctx, "default", resourceName, componentName, updatedConfig, map[string]string{
		"updated": "true",
	})
	if err != nil {
		t.Fatalf("Set() update error = %v", err)
	}

	// Verify updated config
	config, err := store.Get(ctx, "default", resourceName, componentName)
	if err != nil {
		t.Fatalf("Get() error = %v", err)
	}
	if config != updatedConfig {
		t.Errorf("Get() = %v, want %v", config, updatedConfig)
	}

	// Verify labels were updated
	cm := &corev1.ConfigMap{}
	cmName := store.buildConfigMapName(resourceName, componentName)
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      cmName,
	}, cm)
	if err != nil {
		t.Fatalf("ConfigMap not found: %v", err)
	}
	if cm.Labels["updated"] != "true" {
		t.Errorf("ConfigMap label not updated")
	}
}

func TestConfigStore_Delete(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()
	store := NewConfigStore(fakeClient)
	ctx := context.Background()

	resourceName := "test-cluster"
	componentName := "router"

	// Create config
	err := store.Set(ctx, "default", resourceName, componentName, `{"test": "data"}`, nil)
	if err != nil {
		t.Fatalf("Set() error = %v", err)
	}

	// Delete config
	err = store.Delete(ctx, "default", resourceName, componentName)
	if err != nil {
		t.Fatalf("Delete() error = %v", err)
	}

	// Verify ConfigMap is deleted
	cm := &corev1.ConfigMap{}
	cmName := store.buildConfigMapName(resourceName, componentName)
	err = fakeClient.Get(ctx, client.ObjectKey{
		Namespace: "default",
		Name:      cmName,
	}, cm)
	if err == nil {
		t.Errorf("ConfigMap still exists after Delete()")
	}

	// Delete non-existent ConfigMap should not error
	err = store.Delete(ctx, "default", "non-existent", "component")
	if err != nil {
		t.Errorf("Delete() on non-existent ConfigMap error = %v, want nil", err)
	}
}

func TestConfigStore_BuildConfigMapName(t *testing.T) {
	store := &ConfigStore{}

	tests := []struct {
		resourceName  string
		componentName string
		want          string
	}{
		{"cluster-prod", "router", "neutree-cluster-prod-router-config"},
		{"endpoint-demo", "deployment", "neutree-endpoint-demo-deployment-config"},
		{"test", "metrics", "neutree-test-metrics-config"},
	}

	for _, tt := range tests {
		t.Run(tt.resourceName+"-"+tt.componentName, func(t *testing.T) {
			got := store.buildConfigMapName(tt.resourceName, tt.componentName)
			if got != tt.want {
				t.Errorf("buildConfigMapName() = %v, want %v", got, tt.want)
			}
		})
	}
}
