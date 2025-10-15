package router

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestBuildRouterDeployment(t *testing.T) {
	// Test implementation goes here
	routerComponent := &RouterComponent{
		clusterName:     "test-cluster",
		workspace:       "test-workspace",
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
		config: v1.KubernetesClusterConfig{
			Route: v1.RouteSpec{
				AccessMode: v1.KubernetesAccessModeLoadBalancer,
				Version:    "1.0.0",
				Replicas:   2,
				Resources:  map[string]string{"cpu": "100m", "memory": "128Mi"},
			},
		},
	}

	deployment, err := routerComponent.buildRouterDeployment()
	if err != nil {
		t.Fatalf("Failed to build router deployment: %v", err)
	}

	if deployment.GetName() != "router" {
		t.Errorf("Expected deployment name 'router-deployment', got '%s'", deployment.GetName())
	}

	if deployment.GetLabels()["app"] != "router" {
		t.Errorf("Expected label 'app=router', got '%s'", deployment.GetLabels()["app"])
	}

	// Additional assertions can be added here to validate the deployment spec
}
