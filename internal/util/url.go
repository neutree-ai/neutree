package util

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// GetExternalAccessUrl attempts to resolve the external access URL by detecting if it is running
// inside a Kubernetes cluster; if resolution via the cluster Service fails, the original URL is returned.
func GetExternalAccessUrl(accessUrl string) (string, error) {
	restConfig, err := rest.InClusterConfig()
	if err == nil {
		// If running in a cluster, we will first try to find the access URL from the service.
		// If the service lookup (Services().Get) fails, we will return the original access URL;
		// if the service is found but is misconfigured (e.g. LoadBalancer without ingress/IP/Hostname), an error is returned,
		// because the original address is unreachable from the outside.
		kubeClient, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return "", err
		}

		parse, err := url.Parse(accessUrl)
		if err != nil {
			return "", err
		}

		s, err := kubeClient.CoreV1().Services(os.Getenv("NAMESPACE")).Get(context.Background(), parse.Hostname(), metav1.GetOptions{})
		if err == nil {
			var externalIP string
			// todo: current only support http, need to support https in the future
			if s.Spec.Type == corev1.ServiceTypeLoadBalancer {
				if len(s.Status.LoadBalancer.Ingress) == 0 {
					return "", errors.New("load balancer ingress not found")
				}

				switch {
				case s.Status.LoadBalancer.Ingress[0].Hostname != "":
					externalIP = s.Status.LoadBalancer.Ingress[0].Hostname
				case s.Status.LoadBalancer.Ingress[0].IP != "":
					externalIP = s.Status.LoadBalancer.Ingress[0].IP
				default:
					return "", errors.New("load balancer ingress hostname/ip not found")
				}
			} else {
				externalIP = s.Spec.ClusterIP
			}

			port := parse.Port()
			if port == "" {
				parse.Host = externalIP
			} else {
				parse.Host = fmt.Sprintf("%s:%s", externalIP, port)
			}

			return parse.String(), nil
		} else {
			// If service lookup fails, the original address may already be an external address, and no additional processing is required.
			klog.V(4).Infof("failed to get service %s from kubernetes cluster: %v, use original access url %s", parse.Hostname(), err, accessUrl)
		}
	}

	return accessUrl, nil
}
