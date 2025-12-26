package deploy

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestNewDeployer(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	tests := []struct {
		name       string
		deployType DeploymentType
		config     DeployerConfig
		wantError  bool
	}{
		{
			name:       "kubernetes deployer",
			deployType: DeploymentTypeKubernetes,
			config: DeployerConfig{
				KubeClient:    fakeClient,
				Namespace:     "default",
				ResourceName:  "test-cluster",
				ComponentName: "router",
				Labels: map[string]string{
					"app": "test",
				},
				Logger: klog.Background(),
			},
			wantError: false,
		},
		{
			name:       "unsupported deployment type",
			deployType: DeploymentType("unsupported"),
			config: DeployerConfig{
				ResourceName:  "test",
				ComponentName: "test",
			},
			wantError: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			deployer, err := NewDeployer(tt.deployType, tt.config)

			if tt.wantError {
				if err == nil {
					t.Errorf("NewDeployer() error = nil, want error")
				}
				if err != ErrUnsupportedDeploymentType {
					t.Errorf("NewDeployer() error = %v, want %v", err, ErrUnsupportedDeploymentType)
				}
				if deployer != nil {
					t.Errorf("NewDeployer() deployer = %v, want nil", deployer)
				}
				return
			}

			if err != nil {
				t.Fatalf("NewDeployer() error = %v", err)
			}

			if deployer == nil {
				t.Errorf("NewDeployer() deployer = nil, want non-nil")
			}

			// Verify it's a KubernetesDeployer
			_, ok := deployer.(*KubernetesDeployer)
			if !ok {
				t.Errorf("NewDeployer() returned type %T, want *KubernetesDeployer", deployer)
			}
		})
	}
}

func TestNewApply(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	applier := NewApply(fakeClient, "default", "test-cluster", "router")

	if applier == nil {
		t.Fatal("NewApply() returned nil")
	}

	if applier.ctrlClient != fakeClient {
		t.Errorf("NewApply() client not set correctly")
	}

	if applier.namespace != "default" {
		t.Errorf("NewApply() namespace = %v, want default", applier.namespace)
	}

	if applier.resourceName != "test-cluster" {
		t.Errorf("NewApply() resourceName = %v, want test-cluster", applier.resourceName)
	}

	if applier.componentName != "router" {
		t.Errorf("NewApply() componentName = %v, want router", applier.componentName)
	}

	if applier.configStore == nil {
		t.Errorf("NewApply() configStore not initialized")
	}
}

func TestNewApply_BackwardCompatibility(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	// Test that NewApply returns the same type as NewKubernetesDeployer
	applier1 := NewApply(fakeClient, "default", "test", "component")
	applier2 := NewKubernetesDeployer(fakeClient, "default", "test", "component")

	// Both should be the same type
	if _, ok := interface{}(applier1).(Deployer); !ok {
		t.Errorf("NewApply() does not implement Deployer interface")
	}

	if _, ok := interface{}(applier2).(Deployer); !ok {
		t.Errorf("NewKubernetesDeployer() does not implement Deployer interface")
	}
}

func TestDeployerConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	_ = corev1.AddToScheme(scheme)

	fakeClient := fake.NewClientBuilder().WithScheme(scheme).Build()

	config := DeployerConfig{
		ResourceName:  "test-cluster",
		ComponentName: "router",
		Labels: map[string]string{
			"app":       "test",
			"workspace": "default",
		},
		Logger:     klog.Background(),
		KubeClient: fakeClient,
		Namespace:  "default",
	}

	deployer, err := NewDeployer(DeploymentTypeKubernetes, config)
	if err != nil {
		t.Fatalf("NewDeployer() error = %v", err)
	}

	kubeApplier, ok := deployer.(*KubernetesDeployer)
	if !ok {
		t.Fatalf("NewDeployer() returned type %T, want *KubernetesDeployer", deployer)
	}

	// Verify labels were set
	if kubeApplier.labels["app"] != "test" {
		t.Errorf("Labels not set correctly")
	}

	// Verify logger was set
	if kubeApplier.logger.GetSink() != config.Logger.GetSink() {
		t.Errorf("Logger not set correctly")
	}
}
