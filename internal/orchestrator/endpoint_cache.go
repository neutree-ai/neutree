package orchestrator

import (
	"fmt"

	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
)

func (k *kubernetesOrchestrator) configureEndpointCache(ctx *OrchestratorContext, objects *unstructured.UnstructuredList) error {
	if ctx.Endpoint.Spec.KVCache == nil {
		return nil
	}

	provider, err := k.kvCacheProviders.Get(ctx.Endpoint.Spec.KVCache.Provider)
	if err != nil {
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

		if err := provider.Configure(ctx.Endpoint, ctx.Cluster, &dep); err != nil {
			return err
		}

		converted, err := runtime.DefaultUnstructuredConverter.ToUnstructured(&dep)
		if err != nil {
			return err
		}

		obj.Object = converted
		count++
	}

	if count != 1 {
		return fmt.Errorf("KV cache requires a single inference Deployment")
	}

	return nil
}
