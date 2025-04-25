package monitoring

import v1 "github.com/neutree-ai/neutree/api/v1"

// ServiceMonitor defines the interface for services to provide their monitoring configurations
type ServiceMonitor interface {
	MetricsMonitor
}

type MetricsMonitor interface {
	// GetMetricsScrapeTargetsConfig returns the configuration for metrics scraping
	// including target endpoints and identifying labels
	GetMetricsScrapeTargetsConfig() ([]v1.MetricsScrapeTargetsConfig, error)
}
