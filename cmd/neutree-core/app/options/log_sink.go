package options

import (
	"github.com/spf13/pflag"
)

// LogSinkOptions locate where LogSink config is delivered for Vector and how
// Vector is observed. Defaults match the Docker Compose deployment.
type LogSinkOptions struct {
	VectorConfigDir      string
	ConfigMap            string
	Secret               string
	MetricsURL           string
	TracePipelineEnabled bool
}

func NewLogSinkOptions() *LogSinkOptions {
	return &LogSinkOptions{
		VectorConfigDir:      "/etc/neutree/vector",
		ConfigMap:            "neutree-vector-logsinks",
		Secret:               "neutree-vector-logsink-secrets",
		MetricsURL:           "http://vector:9598/metrics",
		TracePipelineEnabled: true,
	}
}

func (o *LogSinkOptions) AddFlags(fs *pflag.FlagSet) {
	fs.StringVar(&o.VectorConfigDir, "vector-config-dir", o.VectorConfigDir,
		"Vector config dir to write log sink config into (outside Kubernetes)")
	fs.StringVar(&o.ConfigMap, "vector-logsinks-configmap", o.ConfigMap,
		"ConfigMap holding the log sink config mounted into Vector (in Kubernetes)")
	fs.StringVar(&o.Secret, "vector-logsink-secret", o.Secret,
		"Secret holding log sink credentials mounted into Vector (in Kubernetes)")
	fs.StringVar(&o.MetricsURL, "vector-metrics-url", o.MetricsURL, "Vector prometheus exporter URL")
	fs.BoolVar(&o.TracePipelineEnabled, "vector-trace-pipeline-enabled", o.TracePipelineEnabled,
		"whether Vector runs the AI trace pipeline that log sinks read from")
}
