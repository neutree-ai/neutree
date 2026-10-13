package cluster

import (
	"fmt"
	"strconv"

	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/clustercache"
)

func (c *NativeKubernetesClusterReconciler) cacheContext(rc *ReconcileContext) (*clustercache.Context, error) {
	prefix, err := util.GetImagePrefix(rc.ImageRegistry)
	if err != nil {
		return nil, err
	}

	kubeconfig, err := util.GetKubeConfigFromCluster(rc.Cluster)
	if err != nil {
		return nil, err
	}

	config, err := clientcmd.RESTConfigFromKubeConfig([]byte(kubeconfig))
	if err != nil {
		return nil, err
	}

	return &clustercache.Context{Cluster: rc.Cluster, Client: rc.ctrClient,
		RESTConfig: config, Namespace: rc.clusterNamespace, ImagePrefix: prefix, ImagePullSecret: ImagePullSecretName,
		SaveStatus: func(status *v1.ClusterStatus) error {
			rc.Cluster.Status = status
			return c.storage.UpdateCluster(strconv.Itoa(rc.Cluster.ID), &v1.Cluster{Status: status})
		}}, nil
}

func (c *NativeKubernetesClusterReconciler) reconcileCache(rc *ReconcileContext) {
	cluster := rc.Cluster
	if cluster.Spec == nil || cluster.Spec.Cache == nil {
		if err := c.deleteCache(rc); err != nil {
			recordCacheError(cluster, err)
		}

		return
	}

	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	if cluster.Status.Cache == nil {
		cluster.Status.Cache = &v1.ClusterCacheStatus{Provider: cluster.Spec.Cache.Provider}
	}

	if cluster.Status.Cache.Provider != cluster.Spec.Cache.Provider {
		recordCacheError(cluster, fmt.Errorf("waiting for previous cache provider cleanup"))
		return
	}

	provider, err := c.cacheProviders.Get(cluster.Spec.Cache.Provider)

	if err == nil {
		var cacheCtx *clustercache.Context

		cacheCtx, err = c.cacheContext(rc)

		if err == nil {
			err = provider.Reconcile(rc.Ctx, cacheCtx)
		}

	}

	cluster.Status.Cache.Error = ""
	if err != nil {
		recordCacheError(cluster, err)
		klog.Warningf("cache reconciliation failed for %s: %v", cluster.Key(), err)
	}

}

func recordCacheError(cluster *v1.Cluster, err error) {
	cluster.Status.Cache.Error = err.Error()
}

func (c *NativeKubernetesClusterReconciler) deleteCache(rc *ReconcileContext) error {
	if rc.Cluster.Status == nil || rc.Cluster.Status.Cache == nil {
		return nil
	}

	provider, err := c.cacheProviders.Get(rc.Cluster.Status.Cache.Provider)

	if err != nil {
		return err
	}

	cacheCtx, err := c.cacheContext(rc)
	if err != nil {
		return err
	}

	if err = provider.Delete(rc.Ctx, cacheCtx); err != nil {
		return err
	}

	rc.Cluster.Status.Cache = nil

	return nil
}
