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
	// Running is set when the component reports utilization, which only a
	// running component does. Other series also appear for a component built
	// during a reload that Vector then rejected, so they prove nothing.
	Running bool

	Sent float64
	// Failed counts failed send attempts: component errors, HTTP client errors
	// (e.g. connection refused, retried forever) and non-2xx responses.
	Failed float64
	// Dropped counts events that will never be delivered.
	Dropped float64
	// Buffered is the number of events in the sink's buffer (a gauge). Events
	// stay counted until delivered, including those waiting in a batch.
	Buffered float64
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
		Running:  c.Running,
		Sent:     d(c.Sent, prev.Sent),
		Failed:   d(c.Failed, prev.Failed),
		Dropped:  d(c.Dropped, prev.Dropped),
		Buffered: c.Buffered,
	}
}

// VectorMetrics is one scrape of Vector's prometheus_exporter.
type VectorMetrics struct {
	// Components holds every log sink component that has series. Series of a
	// removed component stay in the exporter, so presence alone proves little.
	Components map[string]ComponentCounters
	// RejectedReloads counts reloads Vector rejected since it started: config
	// that failed to load (error_code "config_load", e.g. a missing secret) and
	// topology that failed to build (error_code "reload", e.g. a locked buffer).
	// Either way Vector keeps the old config and does not retry on its own.
	// Vector drops series idle for a while, so this can fall back to zero and
	// count up again; compare with RejectedSince.
	RejectedReloads float64
}

// RejectedSince reports whether Vector rejected a reload after base was read.
// A count below base that is not zero means the series expired and came back.
func (m *VectorMetrics) RejectedSince(base float64) bool {
	return m.RejectedReloads > base || (m.RejectedReloads > 0 && m.RejectedReloads < base)
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

			if code := labels["error_code"]; name == "vector_component_errors_total" &&
				(code == "config_load" || code == "reload") {
				m.RejectedReloads += value

				continue
			}

			id := labels["component_id"]
			if !strings.HasPrefix(id, componentPrefix) {
				continue
			}

			c := m.Components[id]

			switch name {
			case "vector_utilization":
				c.Running = true
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
			case "vector_buffer_events":
				c.Buffered += value
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
