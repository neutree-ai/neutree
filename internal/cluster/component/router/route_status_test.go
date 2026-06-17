package router

import (
	"context"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/intstr"

	fakeClient "sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func Test_GetRouterEndpoint(t *testing.T) {
	tests := []struct {
		name             string
		withObject       []runtime.Object
		expectedEndpoint string
		expectError      bool
	}{
		{
			name: "Router service exists",
			withObject: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "router-service",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								Port: 8000,
							},
						},
						Type: corev1.ServiceTypeLoadBalancer,
					},
					Status: corev1.ServiceStatus{
						LoadBalancer: corev1.LoadBalancerStatus{
							Ingress: []corev1.LoadBalancerIngress{
								{
									IP: "192.0.2.1",
								},
							},
						},
					},
				},
			},
			expectedEndpoint: "http://192.0.2.1:8000",
			expectError:      false,
		},
		{
			name:       "Router service does not exist",
			withObject: []runtime.Object{
				// No service object
			},
			expectedEndpoint: "",
			expectError:      true,
		},
		{
			name: "Router service with no ingress ip/hostname",
			withObject: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "router-service",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Type: corev1.ServiceTypeLoadBalancer,
					},
					Status: corev1.ServiceStatus{
						LoadBalancer: corev1.LoadBalancerStatus{
							Ingress: []corev1.LoadBalancerIngress{},
						},
					},
				},
			},
			expectedEndpoint: "",
			expectError:      true,
		},
		{
			name: "Router service with no ingress",
			withObject: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "router-service",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Type: corev1.ServiceTypeLoadBalancer,
					},
					Status: corev1.ServiceStatus{
						LoadBalancer: corev1.LoadBalancerStatus{},
					},
				},
			},
			expectedEndpoint: "",
			expectError:      true,
		},
		{
			name: "Router service of NodePort type",
			withObject: []runtime.Object{
				&corev1.Service{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "router-service",
						Namespace: "default",
					},
					Spec: corev1.ServiceSpec{
						Ports: []corev1.ServicePort{
							{
								NodePort: 30080,
							},
						},
						Type: corev1.ServiceTypeNodePort,
					},
				},
			},
			expectedEndpoint: "http://127.0.0.1:30080",
			expectError:      false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RouterComponent{
				ctrlClient: fakeClient.NewFakeClient(tt.withObject...),
				namespace:  "default",
				config: v1.KubernetesClusterConfig{
					// server address is 127.0.0.1:6443
					Kubeconfig: "YXBpVmVyc2lvbjogdjEKY2x1c3RlcnM6Ci0gY2x1c3RlcjoKICAgIHNlcnZlcjogaHR0cHM6Ly8xMjcuMC4wLjE6NjQ0Mwo=",
				},
			}
			endpoint, err := r.GetRouteEndpoint(context.Background())
			if (err != nil) != tt.expectError {
				t.Errorf("GetRouteEndpoint() error = %v, expectError %v", err, tt.expectError)
				return
			}
			if endpoint != tt.expectedEndpoint {
				t.Errorf("GetRouteEndpoint() = %v, expectedEndpoint %v", endpoint, tt.expectedEndpoint)
			}
		})
	}
}

func Test_checkServiceStatus(t *testing.T) {
	tests := []struct {
		name          string
		service       *corev1.Service
		expectedReady bool
		expectedIP    string
		expectError   bool
	}{
		{
			name: "Service with LoadBalancer and IP",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{
								IP: "192.0.2.1",
							},
						},
					},
				},
			},
			expectedReady: true,
			expectedIP:    "192.0.2.1",
		},
		{
			name: "Service with LoadBalancer but no Ingress",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{},
					},
				},
			},
			expectedReady: false,
			expectedIP:    "",
		},
		{
			name: "Service with LoadBalancer and Hostname",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
				},
				Status: corev1.ServiceStatus{
					LoadBalancer: corev1.LoadBalancerStatus{
						Ingress: []corev1.LoadBalancerIngress{
							{
								Hostname: "example.com",
							},
						},
					},
				},
			},
			expectedReady: true,
			expectedIP:    "example.com",
		},
		{
			name: "Service of ClusterIP type",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeClusterIP,
				},
			},
			expectedReady: true,
			expectedIP:    "",
		},
		{
			name: "Service not found",
			service: &corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-test",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeClusterIP,
				},
			},
			expectedReady: false,
			expectedIP:    "",
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RouterComponent{
				ctrlClient: fakeClient.NewFakeClient(tt.service),
				namespace:  "default",
			}
			ready, ip, err := r.checkServiceStatus(context.Background())
			if tt.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}
			if ready != tt.expectedReady {
				t.Errorf("checkServiceStatus() ready = %v, expected %v", ready, tt.expectedReady)
			}
			if ip != tt.expectedIP {
				t.Errorf("checkServiceStatus() ip = %v, expected %v", ip, tt.expectedIP)
			}
		})
	}
}

func TestCheckResourcesStatusIncludesRouterDiagnostics(t *testing.T) {
	now := metav1.NewTime(time.Date(2026, 6, 17, 10, 0, 0, 0, time.UTC))
	r := &RouterComponent{
		ctrlClient: fakeClient.NewFakeClient(
			&appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "router",
					Namespace:  "default",
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointy.Int32(1),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					Replicas:           1,
					UpdatedReplicas:    1,
					ReadyReplicas:      0,
					AvailableReplicas:  0,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:    appsv1.DeploymentAvailable,
							Status:  corev1.ConditionFalse,
							Reason:  "MinimumReplicasUnavailable",
							Message: "Deployment does not have minimum availability",
						},
					},
				},
			},
			&corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-6f8d9c7b8c-abcde",
					Namespace: "default",
					Labels: map[string]string{
						"app":       "router",
						"cluster":   "test",
						"workspace": "default",
					},
				},
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{
						{
							Name: "router",
							Env: []corev1.EnvVar{
								{Name: "SHOULD_NOT_LEAK", Value: "TOP_SECRET_VALUE"},
							},
						},
					},
				},
				Status: corev1.PodStatus{
					Phase: corev1.PodPending,
					ContainerStatuses: []corev1.ContainerStatus{
						{
							Name: "router",
							State: corev1.ContainerState{
								Waiting: &corev1.ContainerStateWaiting{
									Reason:  "ImagePullBackOff",
									Message: "Back-off pulling image",
								},
							},
						},
					},
				},
			},
			&corev1.Service{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service",
					Namespace: "default",
				},
				Spec: corev1.ServiceSpec{
					Type: corev1.ServiceTypeLoadBalancer,
					Ports: []corev1.ServicePort{
						{
							Name:       "http",
							Port:       8000,
							TargetPort: intstr.FromInt(8000),
							Protocol:   corev1.ProtocolTCP,
						},
					},
				},
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-pull-failed",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Pod",
					Namespace: "default",
					Name:      "router-6f8d9c7b8c-abcde",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "FailedPull",
				Message:       `failed to pull image "example/router:dev"`,
				LastTimestamp: now,
			},
			&corev1.Event{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-service-sync-failed",
					Namespace: "default",
				},
				InvolvedObject: corev1.ObjectReference{
					Kind:      "Service",
					Namespace: "default",
					Name:      "router-service",
				},
				Type:          corev1.EventTypeWarning,
				Reason:        "SyncLoadBalancerFailed",
				Message:       "failed to ensure load balancer",
				LastTimestamp: now,
			},
		),
		namespace: "default",
		cluster: &v1.Cluster{
			Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
			Spec:     &v1.ClusterSpec{Version: "v1.0.0"},
		},
	}

	status, err := r.CheckResourcesStatus(context.Background())

	assert.NoError(t, err)
	assert.False(t, status.DeploymentReady)
	assert.False(t, status.ServiceReady)
	message := status.String()
	assert.Contains(t, message, "deployment/router")
	assert.Contains(t, message, "available=0")
	assert.Contains(t, message, "pod/router-6f8d9c7b8c-abcde")
	assert.Contains(t, message, "ImagePullBackOff")
	assert.Contains(t, message, "event/router-6f8d9c7b8c-abcde")
	assert.Contains(t, message, "FailedPull")
	assert.Contains(t, message, "service/router-service")
	assert.Contains(t, message, "LoadBalancer")
	assert.Contains(t, message, "ingress=<empty>")
	assert.Contains(t, message, "event/router-service")
	assert.Contains(t, message, "SyncLoadBalancerFailed")
	assert.NotContains(t, message, "TOP_SECRET_VALUE")
}

func Test_checkDeploymentStatus(t *testing.T) {
	tests := []struct {
		name          string
		specVersion   string
		objects       []runtime.Object
		expectedReady bool
		expectError   bool
	}{
		{
			name:        "Deployment ready with all pods matching version",
			specVersion: "v1.0.0",
			objects: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name: "router", Namespace: "default", Generation: 2,
					},
					Spec: appsv1.DeploymentSpec{Replicas: pointy.Int32(2)},
					Status: appsv1.DeploymentStatus{
						ObservedGeneration: 2, ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 2, Replicas: 2,
						Conditions: []appsv1.DeploymentCondition{
							{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
							{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "router-aaa", Namespace: "default", Labels: map[string]string{
						"app": "router", "cluster": "test", "workspace": "default",
						v1.NeutreeServingVersionLabel: "v1.0.0",
					}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "router-bbb", Namespace: "default", Labels: map[string]string{
						"app": "router", "cluster": "test", "workspace": "default",
						v1.NeutreeServingVersionLabel: "v1.0.0",
					}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
			},
			expectedReady: true,
		},
		{
			name:        "Deployment ready but pod has old version (rolling update in progress)",
			specVersion: "v1.1.0",
			objects: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name: "router", Namespace: "default", Generation: 2,
					},
					Spec: appsv1.DeploymentSpec{Replicas: pointy.Int32(2)},
					Status: appsv1.DeploymentStatus{
						ObservedGeneration: 2, ReadyReplicas: 2, AvailableReplicas: 2, UpdatedReplicas: 2, Replicas: 2,
						Conditions: []appsv1.DeploymentCondition{
							{Type: appsv1.DeploymentAvailable, Status: corev1.ConditionTrue},
							{Type: appsv1.DeploymentProgressing, Status: corev1.ConditionTrue},
						},
					},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "router-aaa", Namespace: "default", Labels: map[string]string{
						"app": "router", "cluster": "test", "workspace": "default",
						v1.NeutreeServingVersionLabel: "v1.1.0",
					}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
				&corev1.Pod{
					ObjectMeta: metav1.ObjectMeta{Name: "router-bbb", Namespace: "default", Labels: map[string]string{
						"app": "router", "cluster": "test", "workspace": "default",
						v1.NeutreeServingVersionLabel: "v1.0.0",
					}},
					Status: corev1.PodStatus{Phase: corev1.PodRunning},
				},
			},
			expectedReady: false,
		},
		{
			name:        "Deployment not ready (replicas mismatch)",
			specVersion: "v1.0.0",
			objects: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{
						Name: "router", Namespace: "default", Generation: 2,
					},
					Spec:   appsv1.DeploymentSpec{Replicas: pointy.Int32(4)},
					Status: appsv1.DeploymentStatus{ObservedGeneration: 2, ReadyReplicas: 3, AvailableReplicas: 3, UpdatedReplicas: 3, Replicas: 3},
				},
			},
			expectedReady: false,
		},
		{
			name: "Deployment not found",
			objects: []runtime.Object{
				&appsv1.Deployment{
					ObjectMeta: metav1.ObjectMeta{Name: "router-test", Namespace: "default"},
				},
			},
			expectedReady: false,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RouterComponent{
				ctrlClient: fakeClient.NewFakeClient(tt.objects...),
				namespace:  "default",
				cluster: &v1.Cluster{
					Metadata: &v1.Metadata{Name: "test", Workspace: "default"},
					Spec:     &v1.ClusterSpec{Version: tt.specVersion},
				},
			}
			ready, _, _, err := r.checkDeploymentStatus(context.Background())
			if tt.expectError {
				assert.Error(t, err)
				return
			}
			assert.NoError(t, err)
			assert.Equal(t, tt.expectedReady, ready)
		})
	}
}
