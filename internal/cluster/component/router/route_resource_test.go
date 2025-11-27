package router

import (
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func Test_BuildRouterDeployment(t *testing.T) {
	// Test implementation goes here
	routerComponent := &RouterComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{
				Version: "1.0.0",
			},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
		config: v1.KubernetesClusterConfig{
			Router: v1.RouterSpec{
				AccessMode: v1.KubernetesAccessModeLoadBalancer,
				Version:    "1.0.0",
				Replicas:   2,
				Resources:  map[string]string{"cpu": "100m", "memory": "128Mi"},
			},
		},
	}

	objs, err := routerComponent.GetRouteResources()
	if err != nil {
		t.Fatalf("Failed to build router deployment: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind == "Deployment" && obj.GetName() == "router" {
			objContent, _ := json.Marshal(obj.Object)
			deployment := &appsv1.Deployment{}
			err = json.Unmarshal(objContent, deployment)
			if err != nil {
				t.Fatalf("Failed to unmarshal deployment: %v", err)
			}

			assert.Equal(t, "test-namespace", deployment.GetNamespace(), "Deployment namespace mismatch")
			assert.Equal(t, "router", deployment.GetName(), "Deployment name mismatch")
			assert.Equal(t, "router", deployment.GetLabels()["app"], "Deployment app label mismatch")
			assert.Equal(t, int32(2), *deployment.Spec.Replicas, "Deployment replicas mismatch")
			assert.Equal(t, "test-image-prefix/neutree/router:1.0.0", deployment.Spec.Template.Spec.Containers[0].Image, "Deployment image mismatch")
			assert.Equal(t, "test-image-pull-secret", deployment.Spec.Template.Spec.ImagePullSecrets[0].Name, "Deployment image pull secret mismatch")
			actualCPU := deployment.Spec.Template.Spec.Containers[0].Resources.Limits["cpu"]
			actualMemory := deployment.Spec.Template.Spec.Containers[0].Resources.Limits["memory"]
			assert.Equal(t, "100m", actualCPU.String(), "Deployment CPU limit mismatch")
			assert.Equal(t, "128Mi", actualMemory.String(), "Deployment memory limit mismatch")
			return
		}
	}

	t.Fatalf("router deployment not found in resources")
}

func Test_BuildRouterService(t *testing.T) {
	// Test implementation goes here
	routerComponent := &RouterComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{
				Name:      "test-cluster",
				Workspace: "test-workspace",
			},
			Spec: &v1.ClusterSpec{
				Version: "1.0.0",
			},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
		config: v1.KubernetesClusterConfig{
			Router: v1.RouterSpec{
				AccessMode: v1.KubernetesAccessModeLoadBalancer,
				Version:    "1.0.0",
				Replicas:   2,
				Resources:  map[string]string{"cpu": "100m", "memory": "128Mi"},
			},
		},
	}

	objs, err := routerComponent.GetRouteResources()
	if err != nil {
		t.Fatalf("Failed to build router service: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind == "Service" && obj.GetName() == "router-service" {
			objContent, err := json.Marshal(obj.Object)
			assert.NoError(t, err, "Failed to marshal service object")
			service := &corev1.Service{}
			err = json.Unmarshal(objContent, service)
			if err != nil {
				t.Fatalf("Failed to unmarshal service: %v", err)
			}

			assert.Equal(t, "test-namespace", service.GetNamespace(), "Service namespace mismatch")
			assert.Equal(t, "router-service", service.GetName(), "Service name mismatch")
			assert.Equal(t, "router", service.GetLabels()["app"], "Service app label mismatch")
			assert.Equal(t, corev1.ServiceTypeLoadBalancer, service.Spec.Type, "Service type mismatch")
			assert.Equal(t, int32(8000), service.Spec.Ports[0].Port, "Service port mismatch")
			return
		}
	}

	t.Fatalf("router service not found in resources")
}
