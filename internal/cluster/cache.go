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
	if c.cacheProvider == nil {
		if rc.Cluster.Spec != nil && rc.Cluster.Spec.ZCache != nil && rc.Cluster.Spec.ZCache.Enabled {
			recordCacheError(rc.Cluster, fmt.Errorf("ZCache is not supported by this control plane"))
		}

		return
	}

	cacheCtx, err := c.cacheContext(rc)
	if err != nil {
		recordCacheError(rc.Cluster, err)
	} else {
		err = c.cacheProvider.Reconcile(rc.Ctx, cacheCtx)
	}

	if err != nil {
		klog.Warningf("cache reconciliation failed for %s: %v", rc.Cluster.Key(), err)
	}
}

func recordCacheError(cluster *v1.Cluster, err error) {
	if cluster.Status == nil {
		cluster.Status = &v1.ClusterStatus{}
	}

	if cluster.Status.ZCache == nil {
		cluster.Status.ZCache = &v1.ZCacheStatus{}
	}

	cluster.Status.ZCache.Phase = "Failed"
	cluster.Status.ZCache.ObservationError = err.Error()
	cluster.Status.ZCache.Message = err.Error()
}

func (c *NativeKubernetesClusterReconciler) deleteCache(rc *ReconcileContext) error {
	if rc.Cluster.Status == nil || rc.Cluster.Status.ZCache == nil {
		return nil
	}

	if c.cacheProvider == nil {
		return fmt.Errorf("cache cleanup requires a control plane with a cache provider")
	}

	cacheCtx, err := c.cacheContext(rc)
	if err != nil {
		return err
	}

	return c.cacheProvider.Delete(rc.Ctx, cacheCtx)
}
