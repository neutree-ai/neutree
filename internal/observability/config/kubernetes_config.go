package config

import (
	"context"
	"encoding/json"

	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"sigs.k8s.io/controller-runtime/pkg/client/config"

	"github.com/neutree-ai/neutree/internal/observability/monitoring"
)

type KubernetesConfigSync struct {
	metricsConfigMapName string
	configMapNamespace   string

	kubeClient *kubernetes.Clientset
}

func NewKubernetesConfigSync(metricsConfigMapName, configMapNamespace string) (*KubernetesConfigSync, error) {
	kubeClient, err := kubernetes.NewForConfig(config.GetConfigOrDie())
	if err != nil {
		return nil, err
	}

	return &KubernetesConfigSync{
		metricsConfigMapName: metricsConfigMapName,
		configMapNamespace:   configMapNamespace,
		kubeClient:           kubeClient,
	}, nil
}

func (s *KubernetesConfigSync) SyncMetricsCollectConfig(metricsMonitorMap map[string]monitoring.MetricsMonitor) error {
	metricsConfigMap, err := s.kubeClient.CoreV1().ConfigMaps(s.configMapNamespace).Get(context.Background(), s.metricsConfigMapName, metav1.GetOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to get metrics config map: %s", s.metricsConfigMapName)
	}

	newMetricsConfigData := make(map[string]string)

	for key, monitor := range metricsMonitorMap {
		metricsConfigs, err := monitor.GetMetricsScrapeTargetsConfig()
		if err != nil {
			return errors.Wrapf(err, "failed to get metrics configs for key: %s", key)
		}

		configContent, err := json.Marshal(metricsConfigs)
		if err != nil {
			return errors.Wrapf(err, "failed to marshal metrics configs for key: %s", key)
		}

		newMetricsConfigData[key+".json"] = string(configContent)
	}

	metricsConfigMap.Data = newMetricsConfigData

	_, err = s.kubeClient.CoreV1().ConfigMaps(s.configMapNamespace).Update(context.Background(), metricsConfigMap, metav1.UpdateOptions{})
	if err != nil {
		return errors.Wrapf(err, "failed to update metrics config map: %s", s.metricsConfigMapName)
	}

	return nil
}
