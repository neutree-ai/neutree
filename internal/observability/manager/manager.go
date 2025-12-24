package manager

import (
	"context"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/klog/v2"

	"github.com/pkg/errors"

	"github.com/neutree-ai/neutree/internal/observability/config"
	"github.com/neutree-ai/neutree/internal/observability/monitoring"
)

type ObsCollectConfigManager interface {
	GetMetricsCollectConfigManager() MetricsCollectConfigManager
	Start(context.Context)
}

type ObsCollectConfigOptions struct {
	LocalCollectConfigPath                string
	KubernetesMetricsCollectConfigMapName string
	KubernetesCollectConfigNamespace      string
}

func NewObsCollectConfigManager(options ObsCollectConfigOptions) (ObsCollectConfigManager, error) {
	var configSyncer config.ConfigSyncer

	restConfig, err := rest.InClusterConfig()
	if err != nil && err != rest.ErrNotInCluster {
		return nil, err
	}

	if err == rest.ErrNotInCluster {
		configSyncer = config.NewLocalConfigSync(options.LocalCollectConfigPath)
	} else {
		kubeClient, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create kubeclient")
		}

		configSyncer = config.NewKubernetesConfigSync(kubeClient, options.KubernetesMetricsCollectConfigMapName, options.KubernetesCollectConfigNamespace)
	}

	metricsCollectConfigManager := &metricsCollectConfigManager{
		configSyncer:      configSyncer,
		metricsMonitorMap: make(map[string]monitoring.MetricsMonitor),
		lock:              &sync.Mutex{},
	}

	return &obsCollectConfigManager{
		firstSync:                   true,
		metricsCollectConfigManager: metricsCollectConfigManager,
	}, nil
}

type obsCollectConfigManager struct {
	metricsCollectConfigManager MetricsCollectConfigManager

	firstSync bool
}

func (s *obsCollectConfigManager) GetMetricsCollectConfigManager() MetricsCollectConfigManager {
	return s.metricsCollectConfigManager
}

func (s *obsCollectConfigManager) sync() error {
	if s.metricsCollectConfigManager != nil {
		if err := s.metricsCollectConfigManager.Sync(); err != nil {
			return err
		}
	}

	return nil
}

func (s *obsCollectConfigManager) Start(ctx context.Context) {
	wait.UntilWithContext(ctx, func(ctx context.Context) {
		// skip first sync to avoid remove all collect configs.
		if s.firstSync {
			s.firstSync = false
			return
		}

		if err := s.sync(); err != nil {
			klog.Errorf("failed to sync obs collect config: %s", err.Error())
		}
	}, time.Minute)
}
