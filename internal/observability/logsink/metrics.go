package logsink

import (
	"context"
	"io"
	"net/http"
	"strings"

	"github.com/pkg/errors"
	dto "github.com/prometheus/client_model/go"
	"github.com/prometheus/common/expfmt"
)

// ComponentCounters are cumulative Vector counters for one sink component.
type ComponentCounters struct {
	Sent float64
	// Failed counts failed send attempts: component errors, HTTP client errors
	// (e.g. connection refused, retried forever) and non-2xx responses.
	Failed float64
	// Dropped counts events that will never be delivered.
	Dropped float64
}

// Sub returns the change since prev. A counter that went down means Vector
// restarted or rebuilt the series, so the current value is the change.
func (c ComponentCounters) Sub(prev ComponentCounters) ComponentCounters {
	d := func(cur, old float64) float64 {
		if cur < old {
			return cur
		}

		return cur - old
	}

	return ComponentCounters{
		Sent:    d(c.Sent, prev.Sent),
		Failed:  d(c.Failed, prev.Failed),
		Dropped: d(c.Dropped, prev.Dropped),
	}
}

// VectorMetrics is one scrape of Vector's prometheus_exporter.
type VectorMetrics struct {
	// Components holds every log sink component that has series. Series of a
	// removed component stay in the exporter, so presence alone proves little.
	Components map[string]ComponentCounters
	// Reloads counts successful config reloads since Vector started.
	Reloads float64
	// ConfigLoadErrors counts reloads Vector rejected since it started.
	ConfigLoadErrors float64
}

// ScrapeMetrics fetches and parses Vector metrics.
func ScrapeMetrics(ctx context.Context, client *http.Client, url string) (*VectorMetrics, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build metrics request")
	}

	resp, err := client.Do(req)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to scrape %s", url)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("failed to scrape %s: status %d", url, resp.StatusCode)
	}

	return ParseMetrics(resp.Body)
}

// ParseMetrics parses the Prometheus text format exposed by Vector.
func ParseMetrics(r io.Reader) (*VectorMetrics, error) {
	var parser expfmt.TextParser

	families, err := parser.TextToMetricFamilies(r)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse vector metrics")
	}

	m := &VectorMetrics{Components: map[string]ComponentCounters{}}

	for name, family := range families {
		for _, metric := range family.GetMetric() {
			labels := map[string]string{}
			for _, l := range metric.GetLabel() {
				labels[l.GetName()] = l.GetValue()
			}

			value := metricValue(metric)

			switch name {
			case "vector_reloaded_total":
				m.Reloads += value

				continue
			case "vector_component_errors_total":
				if labels["error_code"] == "config_load" {
					m.ConfigLoadErrors += value

					continue
				}
			}

			id := labels["component_id"]
			if !strings.HasPrefix(id, componentPrefix) {
				continue
			}

			c := m.Components[id]

			switch name {
			case "vector_component_sent_events_total":
				c.Sent += value
			case "vector_component_errors_total", "vector_http_client_errors_total":
				c.Failed += value
			case "vector_http_client_responses_total":
				if !strings.HasPrefix(labels["status"], "2") {
					c.Failed += value
				}
			case "vector_component_discarded_events_total", "vector_buffer_discarded_events_total":
				c.Dropped += value
			}

			m.Components[id] = c
		}
	}

	return m, nil
}

func metricValue(m *dto.Metric) float64 {
	switch {
	case m.GetCounter() != nil:
		return m.GetCounter().GetValue()
	case m.GetGauge() != nil:
		return m.GetGauge().GetValue()
	case m.GetUntyped() != nil:
		return m.GetUntyped().GetValue()
	}

	return 0
}
