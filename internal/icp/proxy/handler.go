package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

var hopByHopHeaders = map[string]bool{
	"Connection":          true,
	"Keep-Alive":          true,
	"Proxy-Authenticate":  true,
	"Proxy-Authorization": true,
	"Te":                  true,
	"Trailer":             true,
	"Transfer-Encoding":   true,
	"Upgrade":             true,
}

func isModelListRequest(method, subPath string) bool {
	return method == http.MethodGet && (subPath == "/v1/models" || subPath == "/v1/models/")
}

func passthroughEndpointAllowed(key state.APIKey, route state.Route) bool {
	if key.AllowedModels == nil {
		return true
	}

	for _, m := range key.AllowedModels {
		if (m.Type == "" || m.Type == route.Kind) && (m.EndpointName == "" || m.EndpointName == route.Name) {
			return true
		}
	}

	return false
}

func inspectRestrictedPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	snap *state.ConfigSnapshot,
	route state.Route,
	key state.APIKey,
	maxBytes int64,
) (io.Reader, int64, string, bool) {
	if isModelListRequest(r.Method, route.SubPath) {
		if !passthroughEndpointAllowed(key, route) {
			writeJSONError(w, http.StatusForbidden, "forbidden", "API key is not permitted to access this endpoint")
			return nil, 0, "", false
		}

		return r.Body, r.ContentLength, "", true
	}

	if !state.HasModelRestrictions(snap, key) {
		return r.Body, r.ContentLength, "", true
	}

	if !isJSONContentType(r.Header.Get("Content-Type")) {
		if singleModel, ok := singleRouteModel(route); ok && state.Allowed(snap, key, route.Workspace, singleModel, route.Kind, route.Name) {
			return r.Body, r.ContentLength, singleModel, true
		}

		writeJSONError(w, http.StatusForbidden, "forbidden", "model-restricted API key cannot access non-JSON passthrough route")

		return nil, 0, "", false
	}

	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxBytes))
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_body", err.Error())
		return nil, 0, "", false
	}

	var envelope struct {
		Model string `json:"model"`
	}

	if err = json.Unmarshal(rawBody, &envelope); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_body", err.Error())
		return nil, 0, "", false
	}

	model := strings.TrimSpace(envelope.Model)
	if model == "" || !state.Allowed(snap, key, route.Workspace, model, route.Kind, route.Name) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "model is not allowed for this API key")
		return nil, 0, "", false
	}

	return bytes.NewReader(rawBody), int64(len(rawBody)), model, true
}

func singleRouteModel(route state.Route) (string, bool) {
	if len(route.ModelRoutes) != 1 {
		return "", false
	}

	for model := range route.ModelRoutes {
		if model != "" && model != "*" {
			return model, true
		}
	}

	return "", false
}

func (s *Server) forwardPassthrough(
	w http.ResponseWriter,
	r *http.Request,
	snap *state.ConfigSnapshot,
	route state.Route,
	key state.APIKey,
	reqID string,
) {
	reqBody, contentLen, reqModel, allowed := inspectRestrictedPassthrough(w, r, snap, route, key, s.MaxJSONBodyBytes)
	if !allowed {
		return
	}

	pools := state.PoolIDsForRequest(snap, key, reqModel, route.Kind, route.Name)
	attemptID := reqID + "-pass-1"

	lease, ok := s.acquireLeaseOrReject(w, r.Context(), reqID, attemptID, pools)
	if !ok {
		return
	}

	prefix := strings.TrimSpace(r.Header.Get(headerPrefixHash))
	if prefix != "" {
		prefix = route.Workspace + ":" + route.Key + ":" + prefix
	}

	candidates, modelExists := chooseCandidates(s.State.RuntimeTable(), route, reqModel, prefix)
	if !modelExists {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured on route", reqModel))

		return
	}

	if len(candidates) == 0 {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "no upstream configured for route")

		return
	}

	up := candidates[0].upstream

	upCtx, cancel := s.boundUpstreamContext(r.Context(), lease)
	defer cancel()

	targetURL := joinUpstreamPath(up.URL, route.SubPath, r.URL.RawQuery)

	upReq, err := http.NewRequestWithContext(upCtx, r.Method, targetURL, reqBody)
	if err != nil {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusBadRequest, "bad_request", err.Error())

		return
	}

	upReq.ContentLength = contentLen
	copyHeaders(upReq.Header, r.Header)
	stripUntrustedClientHeaders(upReq.Header)
	upReq.Header.Set(headerRequestID, reqID)

	if prefix != "" {
		upReq.Header.Set(headerPrefixHash, prefix)
	}

	trustedUsage := up.IsTrustedUsage(route.Kind)
	if trustedUsage {
		s.injectUsageToken(upReq.Header, reqID, attemptID, key.ID, route.Key, up.ID, reqModel, pools)
	}

	setUpstreamAuth(upReq.Header, up)

	release := s.State.RuntimeTable().AcquireInFlight(up.ID)
	defer release()

	resp, err := s.Client.Do(upReq)
	if err != nil {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", err.Error())

		return
	}
	defer resp.Body.Close()

	headerUsage, hasHeaderUsage, usageErr := quota.ExtractHeaderUsage(resp.Header)
	if usageErr != nil && trustedUsage {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusBadGateway, "invalid_upstream_usage", usageErr.Error())

		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(headerRequestID, reqID)
	w.WriteHeader(resp.StatusCode)
	flushResponse(w)

	var bodyReader io.Reader = resp.Body
	var sseFilter *quota.SSEUsageFilter

	if strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "text/event-stream") {
		sseFilter = quota.NewSSEUsageFilter(resp.Body)
		bodyReader = sseFilter
	}

	_ = copyStreamWithFlush(w, bodyReader, cancel, s.IdleTimeout)

	rec := quota.UsageRecord{
		RequestID:  reqID,
		AttemptID:  attemptID,
		APIKeyID:   key.ID,
		PoolIDs:    pools,
		RouteKey:   route.Key,
		UpstreamID: up.ID,
		Source:     "passthrough",
	}

	if trustedUsage {
		if hasHeaderUsage && usageErr == nil {
			rec.Measures = headerUsage.Measures
			rec.Source = headerUsage.Source
		} else if sseFilter != nil {
			if u, found := sseFilter.Usage(); found {
				rec.Measures = u.Measures
				rec.Source = u.Source
			}
		}
	}

	if resp.StatusCode >= 400 && len(rec.Measures) == 0 {
		s.cancelLease(lease)
		return
	}

	s.settleLease(lease, quota.BillableUnits(rec.Measures), rec)
}

func (s *Server) forwardTranslated(
	w http.ResponseWriter,
	r *http.Request,
	snap *state.ConfigSnapshot,
	route state.Route,
	key state.APIKey,
	reqID string,
	srcProto string,
) {
	srcCodec, err := protocol.CodecFor(srcProto)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_protocol", err.Error())
		return
	}

	limited := http.MaxBytesReader(w, r.Body, s.MaxJSONBodyBytes)

	req, err := srcCodec.DecodeRequest(limited)
	if err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid_request_body", err.Error())
		return
	}

	if !state.Allowed(snap, key, route.Workspace, req.Model, route.Kind, route.Name) {
		writeJSONError(w, http.StatusForbidden, "forbidden", "model is not allowed for this API key")
		return
	}

	pools := state.PoolIDsForRequest(snap, key, req.Model, route.Kind, route.Name)

	lease, ok := s.acquireLeaseOrReject(w, r.Context(), reqID, reqID+"-try-1", pools)
	if !ok {
		return
	}

	prefixHash := strings.TrimSpace(r.Header.Get(headerPrefixHash))
	if prefixHash != "" {
		prefixHash = route.Workspace + ":" + route.Key + ":" + prefixHash
	} else {
		prefixHash = protocol.PrefixHash(route.Workspace, route.Key, req.Model, req)
	}

	candidates, modelExists := chooseCandidates(s.State.RuntimeTable(), route, req.Model, prefixHash)
	if !modelExists {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusNotFound, "model_not_found", fmt.Sprintf("model %q is not configured on route", req.Model))

		return
	}

	if len(candidates) == 0 {
		s.cancelLease(lease)
		writeJSONError(w, http.StatusBadGateway, "bad_gateway", "no candidate upstream available")

		return
	}

	mr, ok := route.ModelRoutes[req.Model]
	if !ok {
		mr = route.ModelRoutes["*"]
	}

	maxDeliveries := mr.MaxAttempts + 1
	if maxDeliveries <= 0 {
		maxDeliveries = 1
	}

	var unsupportedErr *protocol.UnsupportedFeatureError
	var lastErr error
	lastStatus := http.StatusBadGateway
	deliveries := 0

	for _, c := range candidates {
		if deliveries >= maxDeliveries {
			break
		}

		targetCodec, cErr := protocol.CodecFor(c.upstream.TargetProtocol)
		if cErr != nil {
			lastErr = cErr
			continue
		}

		targetModel := c.target.UpstreamModel
		if targetModel == "" {
			targetModel = req.Model
		}

		wireBody, encErr := targetCodec.EncodeRequest(req, targetModel)
		if encErr != nil {
			var uf *protocol.UnsupportedFeatureError
			if errors.As(encErr, &uf) {
				unsupportedErr = uf
				continue
			}

			s.cancelLease(lease)
			writeJSONError(w, http.StatusBadRequest, "invalid_request", encErr.Error())

			return
		}

		deliveries++
		attemptID := fmt.Sprintf("%s-try-%d", reqID, deliveries)
		trustedUsage := c.upstream.IsTrustedUsage(route.Kind)

		upCtx, cancel := s.boundUpstreamContext(r.Context(), lease)
		release := s.State.RuntimeTable().AcquireInFlight(c.upstream.ID)

		resp, doErr := s.doTranslatedUpstream(upCtx, c.upstream, targetCodec, wireBody, reqID, attemptID, key.ID, route.Key, targetModel, prefixHash, pools, trustedUsage)
		if doErr != nil {
			lastErr = doErr

			s.recordFailedAttempt(reqID, attemptID, key.ID, route.Key, c.upstream.ID, req.Model, "upstream_error")

			release()
			cancel()

			if r.Context().Err() == nil && !s.leaseCancelled(lease) && isRetryableError(doErr, mr.RetryableConditions) {
				continue
			}

			break
		}

		if isRetryableStatus(resp.StatusCode, mr.RetryableConditions) && deliveries < maxDeliveries {
			lastStatus = resp.StatusCode
			s.recordFailedAttempt(reqID, attemptID, key.ID, route.Key, c.upstream.ID, req.Model, fmt.Sprintf("retryable_status_%d", resp.StatusCode))
			_ = resp.Body.Close()

			release()
			cancel()

			continue
		}

		defer release()
		defer cancel()

		s.serveTranslatedResponse(w, resp, cancel, srcCodec, targetCodec, req, lease, reqID, attemptID, key.ID, route.Key, c.upstream.ID, pools, trustedUsage)

		return
	}

	s.cancelLease(lease)

	if deliveries == 0 && unsupportedErr != nil {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(unsupportedErr.StatusCode())
		_, _ = w.Write(unsupportedErr.JSON())

		return
	}

	msg := "all candidate upstreams failed before first byte"
	if lastErr != nil {
		msg = lastErr.Error()
	}

	writeJSONError(w, lastStatus, "upstream_unavailable", msg)
}

func (s *Server) doTranslatedUpstream(
	ctx context.Context,
	up state.Upstream,
	targetCodec protocol.Codec,
	body []byte,
	reqID, attemptID, keyID, routeKey, model, prefixHash string,
	pools []string,
	trustedUsage bool,
) (*http.Response, error) {
	upReq, err := http.NewRequestWithContext(ctx, http.MethodPost, targetCodec.EndpointURL(up.URL), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}

	upReq.Header.Set("Content-Type", "application/json")
	upReq.Header.Set(headerRequestID, reqID)
	upReq.Header.Set(headerPrefixHash, prefixHash)

	if trustedUsage {
		s.injectUsageToken(upReq.Header, reqID, attemptID, keyID, routeKey, up.ID, model, pools)
	}

	setUpstreamAuth(upReq.Header, up)

	return s.Client.Do(upReq)
}

func (s *Server) serveTranslatedResponse(
	w http.ResponseWriter,
	resp *http.Response,
	cancelUpstream context.CancelFunc,
	srcCodec, targetCodec protocol.Codec,
	req protocol.CanonicalRequest,
	lease *quota.Lease,
	reqID, attemptID, keyID, routeKey, upstreamID string,
	pools []string,
	trustedUsage bool,
) {
	defer resp.Body.Close()

	// Always strip internal usage header before returning to client.
	headerUsage, hasHeaderUsage, usageErr := quota.ExtractHeaderUsage(resp.Header)
	hasTrustedHeader := trustedUsage && hasHeaderUsage && usageErr == nil

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		s.cancelLease(lease)
		copyHeaders(w.Header(), resp.Header)
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)

		return
	}

	if !req.Stream {
		decoded, err := targetCodec.DecodeResponse(resp.Body)
		if err != nil {
			s.cancelLease(lease)
			writeJSONError(w, http.StatusBadGateway, "upstream_decode_error", err.Error())

			return
		}

		wire, err := srcCodec.EncodeResponse(decoded, req)
		if err != nil {
			s.cancelLease(lease)
			writeJSONError(w, http.StatusBadGateway, "response_encode_error", err.Error())

			return
		}

		measures := decoded.Usage
		source := "protocol"

		if hasTrustedHeader {
			measures = headerUsage.Measures
			source = headerUsage.Source
		}

		s.settleLease(lease, quota.BillableUnits(measures), quota.UsageRecord{
			RequestID:  reqID,
			AttemptID:  attemptID,
			APIKeyID:   keyID,
			PoolIDs:    pools,
			RouteKey:   routeKey,
			UpstreamID: upstreamID,
			Model:      req.Model,
			Measures:   measures,
			Source:     source,
		})

		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)

		return
	}

	s.streamTranslatedSSE(
		w, resp.Body, cancelUpstream, srcCodec, targetCodec, req,
		lease, reqID, attemptID, keyID, routeKey, upstreamID,
		pools, headerUsage, hasTrustedHeader, trustedUsage,
	)
}

func (s *Server) streamTranslatedSSE(
	w http.ResponseWriter,
	upstreamBody io.Reader,
	cancelUpstream context.CancelFunc,
	srcCodec, targetCodec protocol.Codec,
	req protocol.CanonicalRequest,
	lease *quota.Lease,
	reqID, attemptID, keyID, routeKey, upstreamID string,
	pools []string,
	headerUsage quota.UsageRecord,
	hasTrustedHeader bool,
	trustedUsage bool,
) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.WriteHeader(http.StatusOK)
	flushResponse(w)

	itr := newIdleTimeoutReader(upstreamBody, cancelUpstream, s.IdleTimeout)
	defer itr.Close()

	sseFilter := quota.NewSSEUsageFilter(itr)
	enc := srcCodec.NewStreamEncoder(flushingWriter{w: w}, req)

	var streamMeasures []quota.Measure
	var streamedUnits int64
	errQuotaCutoff := errors.New("stream_quota_cutoff")

	decodeErr := targetCodec.DecodeStream(sseFilter, func(ev protocol.StreamEvent) error {
		if ev.Type == protocol.EventUsage && len(ev.Usage) > 0 {
			streamMeasures = ev.Usage
		}

		delta := estimateEventUnits(ev)
		if delta > 0 {
			if s.Quota != nil && lease != nil {
				if cErr := s.Quota.StepCharge(lease.Context(), lease.ID, delta); cErr != nil {
					cancelUpstream()
					writeStreamCutoffError(w, enc)

					return errQuotaCutoff
				}
			}

			streamedUnits += delta
		}

		if s.leaseCancelled(lease) {
			cancelUpstream()
			writeStreamCutoffError(w, enc)

			return errQuotaCutoff
		}

		return enc.WriteEvent(ev)
	})

	if !errors.Is(decodeErr, errQuotaCutoff) {
		switch {
		case s.leaseCancelled(lease):
			writeStreamCutoffError(w, enc)
		case decodeErr != nil:
			writeStreamError(w, enc, decodeErr)
		default:
			closeStreamEncoder(w, enc)
		}
	}

	measures := streamMeasures
	source := "protocol_stream"

	if trustedUsage {
		if u, found := sseFilter.Usage(); found {
			measures = u.Measures
			source = u.Source
		} else if hasTrustedHeader {
			measures = headerUsage.Measures
			source = headerUsage.Source
		}
	}

	actual := quota.BillableUnits(measures)
	if actual <= 0 {
		actual = streamedUnits

		if len(measures) == 0 && streamedUnits > 0 {
			measures = []quota.Measure{{Metric: "output_tokens", Unit: "tokens", Quantity: streamedUnits}}
		}
	}

	s.settleLease(lease, actual, quota.UsageRecord{
		RequestID:  reqID,
		AttemptID:  attemptID,
		APIKeyID:   keyID,
		PoolIDs:    pools,
		RouteKey:   routeKey,
		UpstreamID: upstreamID,
		Model:      req.Model,
		Measures:   measures,
		Source:     source,
	})
}

func estimateEventUnits(ev protocol.StreamEvent) int64 {
	n := len(ev.Text)
	if ev.ToolCall != nil {
		n += len(ev.ToolCall.Arguments)
	}

	if n == 0 {
		return 0
	}

	units := int64((n + 3) / 4)
	if units < 1 {
		units = 1
	}

	return units
}

func writeStreamCutoffError(w http.ResponseWriter, enc protocol.StreamEncoder) {
	_ = enc.WriteError(quota.ErrQuotaExceeded)

	flushResponse(w)
}

func writeStreamError(w http.ResponseWriter, enc protocol.StreamEncoder, err error) {
	_ = enc.WriteError(err)

	flushResponse(w)
}

func (s *Server) recordFailedAttempt(reqID, attemptID, keyID, routeKey, upstreamID, model, source string) {
	if s.Quota == nil {
		return
	}

	s.Quota.RecordFailedAttempt(quota.UsageRecord{
		RequestID:  reqID,
		AttemptID:  attemptID,
		APIKeyID:   keyID,
		RouteKey:   routeKey,
		UpstreamID: upstreamID,
		Model:      model,
		Source:     source,
	})
}

func closeStreamEncoder(w http.ResponseWriter, enc protocol.StreamEncoder) {
	_ = enc.Close()

	flushResponse(w)
}

func (s *Server) acquireLeaseOrReject(w http.ResponseWriter, ctx context.Context, reqID, attemptID string, pools []string) (*quota.Lease, bool) {
	if s.Quota == nil {
		return nil, true
	}

	lease, err := s.Quota.Reserve(ctx, reqID, attemptID, pools, 1)
	if err != nil {
		writeJSONError(w, http.StatusTooManyRequests, "quota_exceeded", err.Error())
		return nil, false
	}

	return lease, true
}

func (s *Server) boundUpstreamContext(parent context.Context, lease *quota.Lease) (context.Context, context.CancelFunc) {
	ctx, cancel := context.WithCancel(parent)
	if lease == nil || lease.Context() == nil {
		return ctx, cancel
	}

	stop := context.AfterFunc(lease.Context(), cancel)

	return ctx, func() {
		stop()
		cancel()
	}
}

func (s *Server) leaseCancelled(lease *quota.Lease) bool {
	return lease != nil && lease.Context() != nil && lease.Context().Err() != nil
}

func (s *Server) cancelLease(lease *quota.Lease) {
	if s.Quota != nil && lease != nil {
		_ = s.Quota.Cancel(lease.ID)
	}
}

func (s *Server) settleLease(lease *quota.Lease, actual int64, rec quota.UsageRecord) {
	if s.Quota != nil && lease != nil {
		_ = s.Quota.Settle(lease.ID, actual, rec)
	}
}

func (s *Server) injectUsageToken(h http.Header, reqID, attemptID, keyID, routeKey, upstreamID, model string, pools []string) {
	if len(s.CallbackSecret) == 0 {
		return
	}

	tok, err := quota.SignUsageToken(quota.UsageRecord{
		RequestID:  reqID,
		AttemptID:  attemptID,
		APIKeyID:   keyID,
		PoolIDs:    pools,
		RouteKey:   routeKey,
		UpstreamID: upstreamID,
		Model:      model,
	}, time.Now().Add(s.TokenTTL), s.CallbackSecret)
	if err == nil {
		h.Set(headerUsageToken, tok)
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vals := range src {
		if hopByHopHeaders[http.CanonicalHeaderKey(k)] {
			continue
		}

		dst[k] = append([]string(nil), vals...)
	}
}

func setUpstreamAuth(h http.Header, u state.Upstream) {
	h.Del("Authorization")
	h.Del("x-api-key")

	if u.Credential == "" {
		if u.TargetProtocol == protocol.ProtocolAnthropicMessages {
			h.Set("anthropic-version", "2023-06-01")
		}

		return
	}

	if u.AuthType == "bearer" {
		h.Set("Authorization", "Bearer "+u.Credential)
		return
	}

	if u.TargetProtocol == protocol.ProtocolAnthropicMessages {
		h.Set("x-api-key", u.Credential)
		h.Set("anthropic-version", "2023-06-01")

		return
	}

	h.Set("Authorization", u.Credential)
}

func joinUpstreamPath(base, sub, rawQuery string) string {
	base = strings.TrimRight(base, "/")
	if strings.HasSuffix(base, "/v1") && (sub == "/v1" || strings.HasPrefix(sub, "/v1/")) {
		sub = strings.TrimPrefix(sub, "/v1")
	}

	var full string
	if sub == "" || sub == "/" {
		full = base
	} else {
		full = base + "/" + strings.TrimLeft(sub, "/")
	}

	if rawQuery != "" {
		full += "?" + rawQuery
	}

	return full
}
