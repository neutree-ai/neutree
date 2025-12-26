package deploy

import (
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DeployerConfig holds configuration for creating deployers
type DeployerConfig struct {
	ResourceName  string
	ComponentName string
	Labels        map[string]string
	Logger        klog.Logger

	// Kubernetes fields
	KubeClient client.Client
	Namespace  string
}

// NewDeployer creates a deployer based on the deployment type
func NewDeployer(deployType DeploymentType, config DeployerConfig) (Deployer, error) {
	if deployType != DeploymentTypeKubernetes {
		return nil, ErrUnsupportedDeploymentType
	}

	deployer := NewKubernetesDeployer(
		config.KubeClient,
		config.Namespace,
		config.ResourceName,
		config.ComponentName,
	)

	if config.Labels != nil {
		deployer.WithLabels(config.Labels)
	}

	if config.Logger.GetSink() != nil {
		deployer.WithLogger(config.Logger)
	}

	return deployer, nil
}

// NewApply is a convenience function that creates a Kubernetes deployer
// This maintains backward compatibility with existing code
func NewApply(ctrlClient client.Client, namespace, resourceName, componentName string) *KubernetesDeployer {
	return NewKubernetesDeployer(ctrlClient, namespace, resourceName, componentName)
}
