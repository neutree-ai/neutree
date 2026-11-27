package proxy

import (
	"encoding/json"
	"sync"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
)

type RequestTrace struct {
	RequestID string             `json:"request_id"`
	RouteKey  string             `json:"route_key"`
	Track     Track              `json:"track"`
	Model     string             `json:"model,omitempty"`
	Status    int                `json:"status"`
	Duration  time.Duration      `json:"duration"`
	TTFT      time.Duration      `json:"ttft,omitempty"`
	Attempts  []AttemptSummary   `json:"attempts,omitempty"`
	Usage     *quota.UsageRecord `json:"usage,omitempty"`
}

type TelemetrySnapshot struct {
	Requests       int64          `json:"requests"`
	Completed      int64          `json:"completed"`
	Retries        int64          `json:"retries"`
	TotalDuration  time.Duration  `json:"total_duration"`
	TotalTTFT      time.Duration  `json:"total_ttft"`
	RecentRequests []RequestTrace `json:"recent_requests"`
}

type TelemetryPlugin struct {
	mu        sync.Mutex
	stats     TelemetrySnapshot
	maxRecent int
}

func NewTelemetryPlugin() *TelemetryPlugin {
	return &TelemetryPlugin{maxRecent: 128}
}

func (p *TelemetryPlugin) Name() string {
	return "telemetry"
}

func (p *TelemetryPlugin) Begin(_ *RequestContext) RequestHook {
	p.mu.Lock()
	p.stats.Requests++
	p.mu.Unlock()

	return &telemetrySession{
		plugin:  p,
		started: time.Now(),
	}
}

func (p *TelemetryPlugin) Snapshot() TelemetrySnapshot {
	p.mu.Lock()
	defer p.mu.Unlock()

	out := p.stats
	out.RecentRequests = make([]RequestTrace, len(p.stats.RecentRequests))

	for i, r := range p.stats.RecentRequests {
		copied := r
		copied.Attempts = append([]AttemptSummary(nil), r.Attempts...)

		if r.Usage != nil {
			u := *r.Usage
			u.PoolIDs = append([]string(nil), r.Usage.PoolIDs...)
			u.Measures = append([]quota.Measure(nil), r.Usage.Measures...)
			copied.Usage = &u
		}

		out.RecentRequests[i] = copied
	}

	return out
}

func (p *TelemetryPlugin) MarshalJSON() ([]byte, error) {
	return json.Marshal(p.Snapshot())
}

type telemetrySession struct {
	plugin      *TelemetryPlugin
	started     time.Time
	firstOutput time.Time
	attempts    []AttemptSummary
}

func (s *telemetrySession) OnIngress(_ *RequestContext) *Response {
	return nil
}

func (s *telemetrySession) OnRequest(_ *RequestContext) *Response {
	return nil
}

func (s *telemetrySession) OnAttemptStart(_ *AttemptContext) *Response {
	return nil
}

func (s *telemetrySession) OnAttemptResponse(_ *AttemptContext) *Response {
	return nil
}

func (s *telemetrySession) OnCanonicalResponse(_ *AttemptContext, _ *protocol.CanonicalResponse) *Response {
	return nil
}

func (s *telemetrySession) OnStreamEvent(_ *AttemptContext, ev *protocol.StreamEvent) error {
	if s.firstOutput.IsZero() && (ev.Text != "" || ev.ToolCall != nil) {
		s.firstOutput = time.Now()
	}

	return nil
}

func (s *telemetrySession) OnAttemptDone(ac *AttemptContext) {
	summary := AttemptSummary{
		ID:          ac.ID,
		UpstreamID:  ac.Upstream.ID,
		TargetModel: ac.TargetModel,
		Status:      ac.StatusCode,
		Disposition: ac.Disposition,
		Synthetic:   ac.Synthetic,
	}

	if ac.Err != nil {
		summary.Err = ac.Err.Error()
	}

	if !ac.StartedAt.IsZero() {
		summary.Duration = time.Since(ac.StartedAt)

		if !s.firstOutput.IsZero() && ac.Disposition == AttemptDeliver {
			summary.TTFT = s.firstOutput.Sub(ac.StartedAt)
		} else if !ac.FirstByteAt.IsZero() {
			summary.TTFT = ac.FirstByteAt.Sub(ac.StartedAt)
		}
	}

	s.attempts = append(s.attempts, summary)
}

func (s *telemetrySession) OnComplete(rc *RequestContext) {
	dur := time.Since(s.started)

	var ttft time.Duration
	var retries int64

	for _, a := range s.attempts {
		if a.Disposition == AttemptRetry {
			retries++
		}

		if a.Disposition == AttemptDeliver && a.TTFT > 0 {
			ttft = a.TTFT
		}
	}

	var usageCopy *quota.UsageRecord

	if rc.Usage != nil {
		u := *rc.Usage
		u.PoolIDs = append([]string(nil), rc.Usage.PoolIDs...)
		u.Measures = append([]quota.Measure(nil), rc.Usage.Measures...)
		usageCopy = &u
	}

	trace := RequestTrace{
		RequestID: rc.RequestID,
		RouteKey:  rc.Route.Key,
		Track:     rc.Track,
		Model:     rc.Model,
		Status:    rc.ResponseStatus,
		Duration:  dur,
		TTFT:      ttft,
		Attempts:  append([]AttemptSummary(nil), s.attempts...),
		Usage:     usageCopy,
	}

	s.plugin.mu.Lock()
	s.plugin.stats.Completed++
	s.plugin.stats.Retries += retries
	s.plugin.stats.TotalDuration += dur
	s.plugin.stats.TotalTTFT += ttft
	s.plugin.stats.RecentRequests = append(s.plugin.stats.RecentRequests, trace)

	if n := len(s.plugin.stats.RecentRequests); n > s.plugin.maxRecent {
		s.plugin.stats.RecentRequests = s.plugin.stats.RecentRequests[n-s.plugin.maxRecent:]
	}
	s.plugin.mu.Unlock()
}
