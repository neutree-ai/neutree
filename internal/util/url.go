package util

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"
)

// GetExternalAccessUrl attempts to resolve the external access URL by detecting if it is running
// inside a Kubernetes cluster; if resolution via the cluster Service fails, the original URL is returned.
// If the Service is found but is misconfigured (e.g. a NodePort without NODEPORT_EXTERNAL_HOST set),
// an error is returned because the resolved address would be unreachable from the outside.
func GetExternalAccessUrl(accessUrl string) (string, error) {
	restConfig, err := rest.InClusterConfig()
	if err == nil {
		kubeClient, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return "", err
		}

		return resolveExternalAccessUrl(kubeClient, os.Getenv("NAMESPACE"), os.Getenv("NODEPORT_EXTERNAL_HOST"), accessUrl)
	}

	return accessUrl, nil
}

// resolveExternalAccessUrl resolves an in-cluster Service URL to an externally reachable address.
// If running in a cluster, we will first try to find the access URL from the service.
// If the service lookup (Services().Get) fails, we will return the original access URL;
// if the service is found but is misconfigured (e.g. LoadBalancer without ingress/IP/Hostname,
// or NodePort without a paired nodePort), an error is returned, because the original address is unreachable from the outside.
func resolveExternalAccessUrl(client kubernetes.Interface, namespace, externalHost, accessUrl string) (string, error) {
	parse, err := url.Parse(accessUrl)
	if err != nil {
		return "", err
	}

	s, err := client.CoreV1().Services(namespace).Get(context.Background(), parse.Hostname(), metav1.GetOptions{})
	if err != nil {
		// If service lookup fails, the original address may already be an external address, and no additional processing is required.
		klog.V(4).Infof("failed to get service %s from kubernetes cluster: %v, use original access url %s", parse.Hostname(), err, accessUrl)
		return accessUrl, nil
	}

	// For NodePort Services the externally reachable address is the configured external host
	// plus the NodePort paired with the URL's service port; the service port itself (e.g. 80)
	// is only reachable inside the cluster.
	if s.Spec.Type == corev1.ServiceTypeNodePort {
		port := parse.Port()
		if port == "" {
			return "", fmt.Errorf("nodeport service %s requires an explicit port in the URL", s.Name)
		}

		if externalHost == "" {
			return "", errors.New("nodeport external host not set, set the NODEPORT_EXTERNAL_HOST environment variable")
		}

		urlPort, err := strconv.ParseInt(port, 10, 32)
		if err != nil {
			return "", fmt.Errorf("invalid port %q in URL for nodeport service %s", port, s.Name)
		}

		var nodePort int32

		for _, p := range s.Spec.Ports {
			if int64(p.Port) == urlPort {
				nodePort = p.NodePort
				break
			}
		}

		if nodePort == 0 {
			return "", fmt.Errorf("no nodePort assigned for service %s port %s", s.Name, port)
		}

		parse.Host = net.JoinHostPort(externalHost, strconv.Itoa(int(nodePort)))

		return parse.String(), nil
	}

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
}
