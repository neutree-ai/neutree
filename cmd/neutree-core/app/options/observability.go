package options

import (
	"github.com/spf13/pflag"
)

type ObservabilityOptions struct {
	LocalCollectConfigPath            string
	KubernetesMetricsCollectConfigMap string
	KubernetesCollectConfigNamespace  string
	MetricsRemoteWriteURL             string
}

func NewObservabilityOptions() *ObservabilityOptions {
	return &ObservabilityOptions{
		LocalCollectConfigPath:            "/etc/neutree/collect",
		KubernetesMetricsCollectConfigMap: "vmagent-scrape-config",
		KubernetesCollectConfigNamespace:  "neutree",
		MetricsRemoteWriteURL:             "",
	}
}

func (o *ObservabilityOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.LocalCollectConfigPath, "local-collect-config-path", o.LocalCollectConfigPath, "local collect config path")
	fs.StringVar(&o.KubernetesMetricsCollectConfigMap, "kubernetes-metrics-collect-configmap", o.KubernetesMetricsCollectConfigMap, "kubernetes collect config name")
	fs.StringVar(&o.KubernetesCollectConfigNamespace, "kubernetes-collect-config-namespace", o.KubernetesCollectConfigNamespace, "kubernetes collect config namespace")
	fs.StringVar(&o.MetricsRemoteWriteURL, "metrics-remote-write-url", o.MetricsRemoteWriteURL, "metrics remote write url")
}

func (o *ObservabilityOptions) Validate() error {
	return nil
}
