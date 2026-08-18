package util

import (
	"context"
	"strings"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes/fake"
)

func TestResolveExternalAccessUrl(t *testing.T) {
	const namespace = "neutree"
	const externalHost = "lb.example.com"

	newNodePortService := func(name string, ports []corev1.ServicePort) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeNodePort,
				ClusterIP: "10.0.0.10",
				Ports:     ports,
			},
		}
	}

	httpNodePort := []corev1.ServicePort{
		{Name: "http", Port: 80, NodePort: 30981},
	}
	httpAndHTTPSNodePort := []corev1.ServicePort{
		{Name: "http", Port: 80, NodePort: 30981},
		{Name: "https", Port: 443, NodePort: 31143},
	}

	newLoadBalancerService := func(name string, hostname, ip string) *corev1.Service {
		svc := &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeLoadBalancer,
				ClusterIP: "10.0.0.20",
				Ports: []corev1.ServicePort{
					{Name: "http", Port: 80},
				},
			},
		}
		if hostname != "" || ip != "" {
			svc.Status.LoadBalancer.Ingress = []corev1.LoadBalancerIngress{{Hostname: hostname, IP: ip}}
		}
		return svc
	}

	newClusterIPService := func(name string) *corev1.Service {
		return &corev1.Service{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: namespace},
			Spec: corev1.ServiceSpec{
				Type:      corev1.ServiceTypeClusterIP,
				ClusterIP: "10.0.0.30",
				Ports: []corev1.ServicePort{
					{Name: "http", Port: 80},
				},
			},
		}
	}

	tests := []struct {
		name         string
		services     []*corev1.Service
		externalHost string
		accessURL    string
		want         string
		wantErrSub   string
	}{
		{
			name:         "nodeport single port rewrites to external host and nodePort",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpNodePort)},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			want:         "http://lb.example.com:30981",
		},
		{
			name:         "nodeport https maps to the tls port nodePort and keeps scheme",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpAndHTTPSNodePort)},
			externalHost: externalHost,
			accessURL:    "https://kong-proxy:443",
			want:         "https://lb.example.com:31143",
		},
		{
			name:         "nodeport preserves path and query",
			services:     []*corev1.Service{newNodePortService("vminsert", httpNodePort)},
			externalHost: externalHost,
			accessURL:    "http://vminsert:80/insert/0/prometheus/?extra=1",
			want:         "http://lb.example.com:30981/insert/0/prometheus/?extra=1",
		},
		{
			name:         "nodeport without external host errors",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpNodePort)},
			externalHost: "",
			accessURL:    "http://kong-proxy:80",
			wantErrSub:   "nodeport external host not set",
		},
		{
			name:         "nodeport URL without port errors",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpAndHTTPSNodePort)},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy",
			wantErrSub:   "requires an explicit port",
		},
		{
			name:         "nodeport URL port not in service errors",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpNodePort)},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:8080",
			wantErrSub:   "no nodePort assigned",
		},
		{
			name:         "nodeport with unassigned nodePort errors",
			services:     []*corev1.Service{newNodePortService("kong-proxy", []corev1.ServicePort{{Name: "http", Port: 80}})},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			wantErrSub:   "no nodePort assigned",
		},
		{
			name:         "loadbalancer hostname keeps existing behavior",
			services:     []*corev1.Service{newLoadBalancerService("kong-proxy", "lb.example.org", "")},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			want:         "http://lb.example.org:80",
		},
		{
			name:         "loadbalancer without ingress errors",
			services:     []*corev1.Service{newLoadBalancerService("kong-proxy", "", "")},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			wantErrSub:   "load balancer ingress not found",
		},
		{
			name:         "clusterip keeps existing behavior",
			services:     []*corev1.Service{newClusterIPService("grafana")},
			externalHost: externalHost,
			accessURL:    "http://grafana:80",
			want:         "http://10.0.0.30:80",
		},
		{
			name:         "clusterip URL without port keeps existing behavior",
			services:     []*corev1.Service{newClusterIPService("grafana")},
			externalHost: externalHost,
			accessURL:    "http://grafana",
			want:         "http://10.0.0.30",
		},
		{
			name:         "loadbalancer ip-only keeps existing behavior",
			services:     []*corev1.Service{newLoadBalancerService("kong-proxy", "", "203.0.113.10")},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			want:         "http://203.0.113.10:80",
		},
		{
			name:         "loadbalancer prefers hostname over ip",
			services:     []*corev1.Service{newLoadBalancerService("kong-proxy", "lb.example.org", "203.0.113.10")},
			externalHost: externalHost,
			accessURL:    "http://kong-proxy:80",
			want:         "http://lb.example.org:80",
		},
		{
			name:         "invalid URL errors",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpNodePort)},
			externalHost: externalHost,
			accessURL:    "http://:badurl",
			wantErrSub:   "invalid port",
		},
		{
			name:         "nodeport with ipv6 external host brackets the host",
			services:     []*corev1.Service{newNodePortService("kong-proxy", httpNodePort)},
			externalHost: "2001:db8::1",
			accessURL:    "http://kong-proxy:80",
			want:         "http://[2001:db8::1]:30981",
		},
		{
			name:         "unknown service returns the original URL",
			services:     []*corev1.Service{},
			externalHost: externalHost,
			accessURL:    "http://missing-svc:80",
			want:         "http://missing-svc:80",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := fake.NewSimpleClientset()
			for _, svc := range tt.services {
				_, err := client.CoreV1().Services(namespace).Create(context.Background(), svc, metav1.CreateOptions{})
				if err != nil {
					t.Fatalf("failed to seed service: %v", err)
				}
			}

			got, err := resolveExternalAccessUrl(client, namespace, tt.externalHost, tt.accessURL)
			if tt.wantErrSub != "" {
				if err == nil {
					t.Fatalf("resolveExternalAccessUrl(%q) = %q; want error containing %q", tt.accessURL, got, tt.wantErrSub)
				}
				if !strings.Contains(err.Error(), tt.wantErrSub) {
					t.Fatalf("resolveExternalAccessUrl(%q) error = %v; want error containing %q", tt.accessURL, err, tt.wantErrSub)
				}
				return
			}

			if err != nil {
				t.Fatalf("resolveExternalAccessUrl(%q) unexpected error: %v", tt.accessURL, err)
			}
			if got != tt.want {
				t.Errorf("resolveExternalAccessUrl(%q) = %q; want %q", tt.accessURL, got, tt.want)
			}
		})
	}
}
