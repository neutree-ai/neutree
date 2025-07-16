package util

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"
)

// GetExternalAccessUrl returns the external access url for kubernetes deploy type
func GetExternalAccessUrl(deployType, accessUrl string) (string, error) {
	if deployType == "kubernetes" {
		kubeClient, err := kubernetes.NewForConfig(config.GetConfigOrDie())
		if err != nil {
			return "", err
		}

		parse, err := url.Parse(accessUrl)
		if err != nil {
			return "", err
		}

		s, err := kubeClient.CoreV1().Services(os.Getenv("NAMESPACE")).Get(context.Background(), parse.Hostname(), metav1.GetOptions{})
		if err != nil {
			return "", err
		}

		var externalIP string
		// todo: current only support http, need to support https in the future
		if s.Spec.Type == "LoadBalancer" {
			if len(s.Status.LoadBalancer.Ingress) == 0 {
				return "", errors.New("load balancer ingress not found")
			}

			if s.Status.LoadBalancer.Ingress[0].IP == "" {
				return "", errors.New("load balancer ingress ip not found")
			}

			externalIP = s.Status.LoadBalancer.Ingress[0].IP
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

	return accessUrl, nil
}
