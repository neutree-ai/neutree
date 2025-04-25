package v1

type MetricsScrapeTargetsConfig struct {
	Labels  map[string]string `json:"labels"`
	Targets []string          `json:"targets"`
}
