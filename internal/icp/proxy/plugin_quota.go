package proxy

import (
	"context"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type QuotaPlugin struct {
	Pool           *quota.LeasePool
	CallbackSecret []byte
	TokenTTL       time.Duration
}

func NewQuotaPlugin(pool *quota.LeasePool, secret []byte, ttl time.Duration) *QuotaPlugin {
	if ttl <= 0 {
		ttl = 10 * time.Minute
	}

	return &QuotaPlugin{
		Pool:           pool,
		CallbackSecret: append([]byte(nil), secret...),
		TokenTTL:       ttl,
	}
}

func (p *QuotaPlugin) Name() string {
	return "quota"
}

func (p *QuotaPlugin) Begin(rc *RequestContext) RequestHook {
	if p.Pool == nil || rc.IsModelList {
		return nil
	}

	return &quotaSession{
		plugin: p,
		rc:     rc,
	}
}

type quotaSession struct {
	plugin           *QuotaPlugin
	rc               *RequestContext
	pools            []string
	lease            *quota.Lease
	stopFreeze       func() bool
	settled          bool
	measures         []quota.Measure
	headerUsage      quota.UsageRecord
	hasTrustedHeader bool
	sseFilter        *quota.SSEUsageFilter
	streamedUnits    int64
}

type filterReadCloser struct {
	io.Reader
	io.Closer
}

func (s *quotaSession) OnIngress(_ *RequestContext) *Response {
	return nil
}

func (s *quotaSession) OnRequest(rc *RequestContext) *Response {
	s.pools = state.PoolIDsForRequest(rc.Snapshot, rc.APIKey, rc.Model, rc.Route.Kind, rc.Route.Name)

	lease, err := s.plugin.Pool.Reserve(rc.Context, rc.RequestID, rc.RequestID+"-reserve", s.pools, 1)
	if err != nil {
		return errorResponse(http.StatusTooManyRequests, "quota_exceeded", "quota_exceeded", err.Error())
	}

	s.lease = lease
	s.stopFreeze = context.AfterFunc(lease.Context(), func() {
		rc.Cancel(quota.ErrQuotaExceeded)
	})

	return nil
}

func (s *quotaSession) OnComplete(_ *RequestContext) {
	s.cancelLease()
}

func (s *quotaSession) releaseFreezeWatch() {
	if s.stopFreeze != nil {
		s.stopFreeze()
		s.stopFreeze = nil
	}
}

func (s *quotaSession) cancelLease() {
	s.releaseFreezeWatch()

	if s.lease != nil && !s.settled {
		_ = s.plugin.Pool.Cancel(s.lease.ID)
		s.settled = true
	}
}

func (s *quotaSession) recordFailedAttempt(ac *AttemptContext, source string) {
	s.plugin.Pool.RecordFailedAttempt(quota.UsageRecord{
		RequestID:  s.rc.RequestID,
		AttemptID:  ac.ID,
		APIKeyID:   s.rc.APIKey.ID,
		RouteKey:   s.rc.Route.Key,
		UpstreamID: ac.Upstream.ID,
		Model:      s.rc.Model,
		Source:     source,
	})
}

func (s *quotaSession) OnAttemptStart(ac *AttemptContext) *Response {
	s.measures = nil
	s.headerUsage = quota.UsageRecord{}
	s.hasTrustedHeader = false
	s.sseFilter = nil
	s.streamedUnits = 0

	if !ac.TrustedUsage || len(s.plugin.CallbackSecret) == 0 || ac.UpstreamRequest == nil {
		return nil
	}

	tok, err := quota.SignUsageToken(quota.UsageRecord{
		RequestID:  s.rc.RequestID,
		AttemptID:  ac.ID,
		APIKeyID:   s.rc.APIKey.ID,
		PoolIDs:    s.pools,
		RouteKey:   s.rc.Route.Key,
		UpstreamID: ac.Upstream.ID,
		Model:      ac.TargetModel,
	}, time.Now().Add(s.plugin.TokenTTL), s.plugin.CallbackSecret)
	if err == nil {
		ac.UpstreamRequest.Header.Set(headerUsageToken, tok)
	}

	return nil
}

func (s *quotaSession) OnAttemptResponse(ac *AttemptContext) *Response {
	if ac.Response == nil {
		return nil
	}

	// Always strip X-Neutree-Usage from upstream headers before client delivery.
	hdrUsage, hasHdr, usageErr := quota.ExtractHeaderUsage(ac.Response.Header)
	if usageErr != nil && ac.TrustedUsage {
		return errorResponse(http.StatusBadGateway, "invalid_upstream_usage", "", usageErr.Error())
	}

	if hasHdr && ac.TrustedUsage {
		s.headerUsage = hdrUsage
		s.hasTrustedHeader = true
	}

	if strings.Contains(strings.ToLower(ac.Response.Header.Get("Content-Type")), "text/event-stream") {
		s.sseFilter = quota.NewSSEUsageFilter(ac.Response.Body)
		ac.Response.Body = filterReadCloser{
			Reader: s.sseFilter,
			Closer: ac.Response.Body,
		}
	}

	return nil
}

func (s *quotaSession) OnCanonicalResponse(_ *AttemptContext, resp *protocol.CanonicalResponse) *Response {
	if len(resp.Usage) > 0 {
		s.measures = resp.Usage
	}

	return nil
}

func (s *quotaSession) OnStreamEvent(_ *AttemptContext, ev *protocol.StreamEvent) error {
	if s.lease == nil {
		return nil
	}

	if s.lease.Context() != nil && s.lease.Context().Err() != nil {
		return quota.ErrQuotaExceeded
	}

	if ev.Type == protocol.EventUsage && len(ev.Usage) > 0 {
		s.measures = ev.Usage
	}

	delta := estimateEventUnits(*ev)
	if delta > 0 {
		if err := s.plugin.Pool.StepCharge(s.lease.Context(), s.lease.ID, delta); err != nil {
			return quota.ErrQuotaExceeded
		}

		s.streamedUnits += delta
	}

	return nil
}

func (s *quotaSession) OnAttemptDone(ac *AttemptContext) {
	if s.lease == nil {
		return
	}

	if ac.Disposition == AttemptRetry {
		source := "upstream_error"
		if ac.StatusCode > 0 && ac.Err == nil {
			source = "retryable_status"
		}

		s.recordFailedAttempt(ac, source)

		return
	}

	if ac.Disposition != AttemptDeliver {
		s.recordFailedAttempt(ac, "attempt_aborted")
		s.cancelLease()

		return
	}

	if s.settled {
		return
	}

	measures, source := s.resolveFinalUsage(ac)
	actual := quota.BillableUnits(measures)

	if actual <= 0 && s.streamedUnits > 0 {
		actual = s.streamedUnits

		if len(measures) == 0 {
			measures = []quota.Measure{{
				Metric:   "output_tokens",
				Unit:     "tokens",
				Quantity: s.streamedUnits,
			}}
		}
	}

	if (ac.StatusCode >= 400 || ac.Err != nil) && len(measures) == 0 {
		s.recordFailedAttempt(ac, "upstream_http_error")
		s.cancelLease()

		return
	}

	rec := quota.UsageRecord{
		RequestID:  s.rc.RequestID,
		AttemptID:  ac.ID,
		APIKeyID:   s.rc.APIKey.ID,
		PoolIDs:    s.pools,
		RouteKey:   s.rc.Route.Key,
		UpstreamID: ac.Upstream.ID,
		Model:      s.rc.Model,
		Measures:   measures,
		Source:     source,
	}

	s.releaseFreezeWatch()

	if err := s.plugin.Pool.Settle(s.lease.ID, actual, rec); err == nil {
		s.settled = true
		s.rc.Usage = &rec
	}
}

func (s *quotaSession) resolveFinalUsage(ac *AttemptContext) ([]quota.Measure, string) {
	measures := s.measures
	source := "protocol"

	if s.rc.Track == TrackPassthrough {
		source = "passthrough"
	} else if ac.Stream {
		source = "protocol_stream"
	}

	if ac.TrustedUsage {
		if s.sseFilter != nil {
			if u, found := s.sseFilter.Usage(); found {
				return u.Measures, u.Source
			}
		}

		if s.hasTrustedHeader {
			return s.headerUsage.Measures, s.headerUsage.Source
		}
	}

	return measures, source
}

func estimateEventUnits(ev protocol.StreamEvent) int64 {
	n := len(ev.Text)
	if ev.ToolCall != nil {
		n += len(ev.ToolCall.Arguments)
	}

	if n == 0 {
		return 0
	}

	return int64((n + 3) / 4)
}
