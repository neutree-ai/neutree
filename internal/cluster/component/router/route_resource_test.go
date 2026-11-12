package router

import (
	"encoding/json"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
)

func Test_BuildRouterDeployment(t *testing.T) {
	// Test implementation goes here
	poolLimit, connectTimeout, socketConnectTimeout, socketReadTimeout := 0, 0.0, 0.25, 600.0
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
				Version:    "1.2.2",
				Replicas:   2,
				Resources:  map[string]string{"cpu": "100m", "memory": "128Mi"},
				RouterProxyConfig: v1.RouterProxyConfig{
					MaxConnectionsPerBackend:  &poolLimit,
					ConnectTimeoutSeconds:     &connectTimeout,
					SockConnectTimeoutSeconds: &socketConnectTimeout,
					SockReadTimeoutSeconds:    &socketReadTimeout,
				},
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
			assert.Equal(t, "test-image-prefix/neutree/router:1.2.2", deployment.Spec.Template.Spec.Containers[0].Image, "Deployment image mismatch")
			assert.Equal(t, "1.0.0", deployment.Spec.Template.Labels["neutree.ai/cluster-version"])
			assert.Equal(t, []string{
				"--host", "0.0.0.0", "--port", "8000", "--k8s-namespace", "test-namespace",
				"--k8s-label-selector", "cluster=test-cluster,workspace=test-workspace,app=inference",
				"--session-key", "session_id", "--service-discovery", "k8s",
				"--max-connections-per-backend", "0", "--connect-timeout-seconds", "0",
				"--sock-connect-timeout-seconds", "0.25", "--sock-read-timeout-seconds", "600",
			}, deployment.Spec.Template.Spec.Containers[0].Args)
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

func Test_BuildRouterDeploymentWithDockerHubKeepsImageUnchanged(t *testing.T) {
	routerComponent := &RouterComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test-cluster", Workspace: "test-workspace"},
			Spec:     &v1.ClusterSpec{Version: "1.0.0"},
		},
		namespace:       "test-namespace",
		imagePrefix:     "docker.io/neutree-ai",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := routerComponent.GetRouteResources()
	if err != nil {
		t.Fatalf("Failed to build router deployment: %v", err)
	}

	for _, obj := range objs.Items {
		if obj.GetObjectKind().GroupVersionKind().Kind != "Deployment" || obj.GetName() != "router" {
			continue
		}

		objContent, _ := json.Marshal(obj.Object)
		deployment := &appsv1.Deployment{}
		if err := json.Unmarshal(objContent, deployment); err != nil {
			t.Fatalf("Failed to unmarshal deployment: %v", err)
		}

		assert.Equal(t, "neutree/router:1.0.0", deployment.Spec.Template.Spec.Containers[0].Image)

		return
	}

	t.Fatalf("router deployment not found in resources")
}

func TestRouterManifestProxyArguments(t *testing.T) {
	cases := []struct {
		name    string
		version string
		router  string
		want    []string
	}{
		{name: "later release renders only supplied option", version: "v1.3.1", router: `{"connect_timeout_seconds":1.5}`, want: []string{"--connect-timeout-seconds", "1.5"}},
		{name: "defaults are owned by router", version: "v1.3.0", router: `{}`},
		{name: "old image omits unsupported options", version: "v1.2.0", router: `{"connect_timeout_seconds":30}`},
		{name: "boundary release omits options", version: "v1.2.1", router: `{"connect_timeout_seconds":30}`},
		{name: "boundary build metadata omits options", version: "v1.2.1+build.1", router: `{"connect_timeout_seconds":30}`},
		{name: "boundary prerelease omits options", version: "v1.2.1-rc.1", router: `{"connect_timeout_seconds":30}`},
		{name: "next patch prerelease renders options", version: "v1.2.2-rc.1", router: `{"connect_timeout_seconds":30}`, want: []string{"--connect-timeout-seconds", "30"}},
		{name: "current test image renders options", version: "v1.3.0-neu869-0", router: `{"connect_timeout_seconds":30}`, want: []string{"--connect-timeout-seconds", "30"}},
		{
			name: "new override supports options on old cluster", version: "v1.2.1",
			router: `{"version":"v1.3.0-neu869-0","connect_timeout_seconds":30}`, want: []string{"--connect-timeout-seconds", "30"},
		},
		{name: "old override omits options on new cluster", version: "v1.3.0", router: `{"version":"v1.2.1","connect_timeout_seconds":30}`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			component := &RouterComponent{
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "cluster", Workspace: "default"},
					Spec:     &v1.ClusterSpec{Version: tc.version},
				},
				namespace:   "default",
				imagePrefix: "docker.io/neutree-ai",
			}
			require.NoError(t, json.Unmarshal([]byte(tc.router), &component.config.Router))
			variables := component.buildManifestVariables()
			assert.Equal(t, tc.want, variables.ProxyArgs)
			assert.Equal(t, tc.version, variables.Version)
		})
	}
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

func TestBuildRouterResourcesWithNumericClusterMetadata(t *testing.T) {
	routerComponent := &RouterComponent{
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "123", Workspace: "456"},
			Spec:     &v1.ClusterSpec{Version: "1.0.0"},
		},
		namespace:       "test-namespace",
		imagePrefix:     "test-image-prefix",
		imagePullSecret: "test-image-pull-secret",
	}

	objs, err := routerComponent.GetRouteResources()
	require.NoError(t, err)

	foundDeployment := false
	foundService := false
	for _, obj := range objs.Items {
		switch obj.GetKind() {
		case "Deployment":
			foundDeployment = true
			deploymentData, err := json.Marshal(obj.Object)
			require.NoError(t, err)

			deployment := &appsv1.Deployment{}
			require.NoError(t, json.Unmarshal(deploymentData, deployment))
			assert.Equal(t, "123", deployment.Spec.Selector.MatchLabels["cluster"])
			assert.Equal(t, "456", deployment.Spec.Selector.MatchLabels["workspace"])
			assert.Equal(t, "123", deployment.Spec.Template.Labels["cluster"])
			assert.Equal(t, "456", deployment.Spec.Template.Labels["workspace"])
		case "Service":
			foundService = true
			serviceData, err := json.Marshal(obj.Object)
			require.NoError(t, err)

			service := &corev1.Service{}
			require.NoError(t, json.Unmarshal(serviceData, service))
			assert.Equal(t, "123", service.Spec.Selector["cluster"])
			assert.Equal(t, "456", service.Spec.Selector["workspace"])
		}
	}

	require.True(t, foundDeployment, "router deployment not found in resources")
	require.True(t, foundService, "router service not found in resources")
}
