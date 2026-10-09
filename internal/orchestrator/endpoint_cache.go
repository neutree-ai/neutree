package orchestrator

import (
	"context"
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/clustercache"
)

const cacheDependencyLabelValue = "true"

func (k *kubernetesOrchestrator) configureEndpointCache(ctx *OrchestratorContext, objects *unstructured.UnstructuredList) error {
	if !clustercache.Enabled(ctx.Endpoint) {
		return nil
	}

	if err := clustercache.Validate(ctx.Endpoint, ctx.Cluster, k.endpointCacheProvider); err != nil {
		return err
	}

	count := 0

	for i := range objects.Items {
		obj := &objects.Items[i]
		if obj.GetKind() != deploymentKind {
			continue
		}

		var dep appsv1.Deployment
		if err := runtime.DefaultUnstructuredConverter.FromUnstructured(obj.Object, &dep); err != nil {
			return err
		}

		if err := k.endpointCacheProvider.Configure(ctx.Endpoint, ctx.Cluster, &dep); err != nil {
			return err
		}

		if dep.Spec.Template.Labels == nil {
			dep.Spec.Template.Labels = map[string]string{}
		}

		dep.Spec.Template.Labels[clustercache.EndpointLabel] = cacheDependencyLabelValue

		converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&dep)
		if err != nil {
			return err
		}

		obj.Object = converted
		count++
	}

	if count != 1 {
		return fmt.Errorf("cluster cache requires a single inference Deployment")
	}

	return nil
}

// Inspect terminating Pods too. A disabled spec or a ready replacement does not
// prove that the old process has stopped using the node-local cache.
func observeEndpointCache(c client.Client, namespace string, endpoint *v1.Endpoint) (*v1.EndpointZCacheStatus, error) {
	status := &v1.EndpointZCacheStatus{Generation: endpoint.Status.ZCache.Generation}
	dep := &appsv1.Deployment{}

	err := c.Get(context.Background(), client.ObjectKey{Namespace: namespace, Name: endpoint.Metadata.Name}, dep)
	if err != nil && !apierrors.IsNotFound(err) {
		return nil, err
	}

	if err == nil && dep.Spec.Template.Labels[clustercache.EndpointLabel] == cacheDependencyLabelValue {
		status.InUse = true
	}

	var pods corev1.PodList
	if err := c.List(context.Background(), &pods, client.InNamespace(namespace),
		client.MatchingLabels{"app": "inference", "endpoint": endpoint.Metadata.Name}); err != nil {
		return nil, err
	}

	for _, pod := range pods.Items {
		if pod.Labels[clustercache.EndpointLabel] == cacheDependencyLabelValue {
			status.InUse = true
		}
	}

	return status, nil
}
