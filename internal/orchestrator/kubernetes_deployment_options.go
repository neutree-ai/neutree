package orchestrator

import (
	"math"

	"github.com/pkg/errors"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// applyDeploymentOptions configures desired objects before diffing and saving them.
func applyDeploymentOptions(objects *unstructured.UnstructuredList, endpoint *v1.Endpoint, namespace, engineName string) error {
	if _, configured := endpoint.Spec.DeploymentOptions[startupTimeoutSecondsKey]; !configured {
		return nil
	}

	timeout, err := resolveStartupTimeoutSeconds(endpoint)
	if err != nil {
		return err
	}

	if err := applyStartupTimeout(objects, namespace, endpoint.Metadata.Name, engineName, int64(timeout)); err != nil {
		return errors.Wrapf(err, "endpoint %s deployment_options.%s cannot be applied", endpoint.Metadata.WorkspaceName(), startupTimeoutSecondsKey)
	}

	return nil
}

func applyStartupTimeout(objects *unstructured.UnstructuredList, namespace, endpointName, engineName string, timeout int64) error {
	if timeout > math.MaxInt32 {
		return errors.Errorf("timeout must not exceed %d seconds", math.MaxInt32)
	}

	deploymentIndex := -1

	for i := range objects.Items {
		obj := &objects.Items[i]
		if obj.GetAPIVersion() != "apps/v1" || obj.GetKind() != deploymentKind || obj.GetNamespace() != namespace || obj.GetName() != endpointName {
			continue
		}

		if deploymentIndex != -1 {
			return errors.Errorf("template must define exactly one apps/v1 Deployment %s/%s", namespace, endpointName)
		}

		deploymentIndex = i
	}

	if deploymentIndex == -1 {
		return errors.Errorf("template must define apps/v1 Deployment %s/%s", namespace, endpointName)
	}

	var deployment appsv1.Deployment
	if err := runtime.DefaultUnstructuredConverter.FromUnstructured(objects.Items[deploymentIndex].Object, &deployment); err != nil {
		return errors.Wrap(err, "template Deployment must be a valid apps/v1 Deployment")
	}

	if timeout <= int64(deployment.Spec.MinReadySeconds) {
		return errors.Errorf("timeout must be greater than Deployment minReadySeconds (%d)", deployment.Spec.MinReadySeconds)
	}

	deadline := int32(timeout)
	deployment.Spec.ProgressDeadlineSeconds = &deadline

	if container := engineContainer(deployment.Spec.Template.Spec.Containers, engineName); container == nil {
		klog.Warningf("Deployment %s/%s has no container named %q, only progressDeadlineSeconds is configured from %s",
			namespace, endpointName, engineName, startupTimeoutSecondsKey)
	} else if probe := container.StartupProbe; probe != nil {
		period := probe.PeriodSeconds
		if period == 0 {
			// Kubernetes applies the default period when it is omitted or zero.
			period = startupProbePeriodSeconds
		}

		if period < 0 {
			return errors.Errorf("startupProbe.periodSeconds must be a positive integer")
		}

		probe.FailureThreshold = (deadline-1)/period + 1
	} else {
		klog.Warningf("Deployment %s/%s container %q has no startupProbe, only progressDeadlineSeconds is configured from %s",
			namespace, endpointName, container.Name, startupTimeoutSecondsKey)
	}

	converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&deployment)
	if err != nil {
		return errors.Wrap(err, "failed to configure startup timeout")
	}

	objects.Items[deploymentIndex].Object = converted

	return nil
}

// engineContainer returns the container running the engine, or nil when the template names it differently.
func engineContainer(containers []corev1.Container, engineName string) *corev1.Container {
	for i := range containers {
		if containers[i].Name == engineName {
			return &containers[i]
		}
	}

	return nil
}
