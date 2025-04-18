package monitoring

type MetricsScrapeTargetsConfig struct {
	Labels  map[string]string `json:"labels"`
	Targets []string          `json:"targets"`
}

// ServiceMonitor defines the interface for services to provide their monitoring configurations
type ServiceMonitor interface {
	MetricsMonitor
}

type MetricsMonitor interface {
	// GetMetricsScrapeTargetsConfig returns the configuration for metrics scraping
	// including target endpoints and identifying labels
	GetMetricsScrapeTargetsConfig() ([]MetricsScrapeTargetsConfig, error)
}
