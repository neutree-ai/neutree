package manager

import (
	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster"
	"github.com/neutree-ai/neutree/internal/observability/config"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
	"github.com/neutree-ai/neutree/pkg/storage"
	"github.com/pkg/errors"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/clientcmd"
	"k8s.io/klog"
)

type MetricsCollectConfigManager interface {
	Sync() error
}

type metricsCollectConfigManager struct {
	localConfigSyncer config.ConfigSyncer
	storage           storage.Storage
}

func (m *metricsCollectConfigManager) Sync() error {
	err := m.syncClusterMetricsCollect()
	if err != nil {
		return errors.Wrapf(err, "failed to sync cluster metrics collect config")
	}

	return nil

}

func (m *metricsCollectConfigManager) syncClusterMetricsCollect() error {
	clusters, err := m.storage.ListCluster(storage.ListOption{})
	if err != nil {
		return err
	}

	sshClusterList := []v1.Cluster{}
	kubernetesClusterList := []v1.Cluster{}
	for _, c := range clusters {
		if c.Spec.Type == v1.SSHClusterType {
			sshClusterList = append(sshClusterList, c)
		} else if c.Spec.Type == v1.KubernetesClusterType {
			kubernetesClusterList = append(kubernetesClusterList, c)
		}
	}

	localSyncClusterMap := map[string][]v1.MetricsScrapeTargetsConfig{}
	for _, c := range sshClusterList {
		if c.Status.DashboardURL == "" {
			continue
		}

		config, err := monitoring.NewClusterMonitor(&c).GetMetricsScrapeTargetsConfig()
		if err != nil {
			klog.Errorf("failed to get metrics scrape targets config for cluster %s: %v", c.Metadata.Name, err)
			continue
		}

		localSyncClusterMap[c.Key()] = config
	}

	err = m.localConfigSyncer.SyncMetricsCollectConfig(localSyncClusterMap)
	if err != nil {
		return err
	}

	for _, c := range kubernetesClusterList {
		if c.Status.DashboardURL == "" {
			continue
		}

		config, err := monitoring.NewClusterMonitor(&c).GetMetricsScrapeTargetsConfig()
		if err != nil {
			klog.Errorf("failed to get metrics scrape targets config for cluster %s: %v", c.Metadata.Name, err)
			continue
		}

		kubeConfigSyncer, err := getKubernetesClusterConfigSyncer(&c)
		if err != nil {
			klog.Errorf("failed to get kubernetes config syncer for cluster %s: %v", c.Metadata.Name, err)
			continue
		}

		err = kubeConfigSyncer.SyncMetricsCollectConfig(map[string][]v1.MetricsScrapeTargetsConfig{
			c.Key(): config,
		})
	}

	return nil
}

func getKubernetesClusterConfigSyncer(c *v1.Cluster) (config.ConfigSyncer, error) {
	clusterConfig, err := cluster.ParseKubernetesClusterConfig(c)
	if err != nil {
		return nil, err
	}

	restConfig, err := clientcmd.RESTConfigFromKubeConfig([]byte(clusterConfig.Kubeconfig))
	if err != nil {
		return nil, err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, err
	}

	// create config syncer
	return config.NewKubernetesConfigSync(clientset, cluster.DefaultMonitorCollectConfigMapName, cluster.Namespace(c)), nil
}
