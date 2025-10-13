package router

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
	"go.openly.dev/pointy"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"

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

func Test_checkDeploymentStatus(t *testing.T) {
	tests := []struct {
		name          string
		deployment    *appsv1.Deployment
		expectedReady bool
		expectError   bool
	}{
		{
			name: "Deployment ready and available",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "router",
					Namespace:  "default",
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointy.Int32(3),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					UpdatedReplicas:    3,
					Replicas:           3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
						{
							Type:   appsv1.DeploymentProgressing,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedReady: true,
			expectError:   false,
		},
		{
			name: "Deployment not ready",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:       "router",
					Namespace:  "default",
					Generation: 2,
				},
				Spec: appsv1.DeploymentSpec{
					Replicas: pointy.Int32(4),
				},
				Status: appsv1.DeploymentStatus{
					ObservedGeneration: 2,
					ReadyReplicas:      3,
					AvailableReplicas:  3,
					UpdatedReplicas:    3,
					Replicas:           3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionTrue,
						},
					},
				},
			},
			expectedReady: false,
			expectError:   false,
		},
		{
			name: "Deployment not found",
			deployment: &appsv1.Deployment{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "router-test",
					Namespace: "default",
				},
				Status: appsv1.DeploymentStatus{
					ReadyReplicas:     1,
					AvailableReplicas: 3,
					Conditions: []appsv1.DeploymentCondition{
						{
							Type:   appsv1.DeploymentAvailable,
							Status: corev1.ConditionFalse,
						},
					},
				},
			},
			expectedReady: false,
			expectError:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			r := &RouterComponent{
				ctrlClient: fakeClient.NewFakeClient(tt.deployment),
				namespace:  "default",
			}
			ready, _, _, err := r.checkDeploymentStatus(context.Background())
			if tt.expectError {
				assert.Error(t, err)
				return
			} else {
				assert.NoError(t, err)
			}
			if ready != tt.expectedReady {
				t.Errorf("checkDeploymentStatus() ready = %v, expected %v", ready, tt.expectedReady)
			}
		})
	}
}
