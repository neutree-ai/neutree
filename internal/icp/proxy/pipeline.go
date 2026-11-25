package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
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
	if method != http.MethodGet {
		return false
	}

	switch strings.TrimRight(subPath, "/") {
	case "/v1/models", "/anthropic/v1/models":
		return true
	default:
		return false
	}
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

func hasConcreteModelRoutes(route state.Route) bool {
	for model := range route.ModelRoutes {
		if model != "" && model != "*" {
			return true
		}
	}

	return false
}

func needsPassthroughModel(snap *state.ConfigSnapshot, route state.Route, key state.APIKey) bool {
	return state.HasModelRestrictions(snap, key) || len(route.ModelRoutes) > 0
}

func (s *Server) preparePassthroughRequest(w http.ResponseWriter, r *http.Request, rc *RequestContext) *Response {
	if rc.IsModelList {
		return nil
	}

	if !hasJSONRequestBody(r) {
		if singleModel, ok := singleRouteModel(rc.Route); ok && rc.Model == "" {
			rc.Model = singleModel
		}

		return nil
	}

	if !needsPassthroughModel(rc.Snapshot, rc.Route, rc.APIKey) {
		return nil
	}

	rawBody, err := io.ReadAll(http.MaxBytesReader(w, r.Body, s.MaxJSONBodyBytes))
	if err != nil {
		return errorResponse(http.StatusBadRequest, "invalid_request_body", "", err.Error())
	}

	rc.RequestBody = bytes.NewReader(rawBody)
	rc.RequestBodyLength = int64(len(rawBody))

	var envelope struct {
		Model string `json:"model"`
	}

	if err = json.Unmarshal(rawBody, &envelope); err != nil {
		if state.HasModelRestrictions(rc.Snapshot, rc.APIKey) || rc.Route.Kind == routeKindExternal {
			return errorResponse(http.StatusBadRequest, "invalid_request_body", "", err.Error())
		}

		return nil
	}

	rc.Model = strings.TrimSpace(envelope.Model)
	if rc.Model == "" && !state.HasModelRestrictions(rc.Snapshot, rc.APIKey) {
		if singleModel, ok := singleRouteModel(rc.Route); ok {
			rc.Model = singleModel
		}
	}

	return nil
}

func (s *Server) executePassthrough(w http.ResponseWriter, r *http.Request, rc *RequestContext, stack *hookStack) {
	if prepResp := s.preparePassthroughRequest(w, r, rc); prepResp != nil {
		writePluginResponse(w, rc, prepResp)
		return
	}

	if shortResp := stack.onRequest(rc); shortResp != nil {
		writePluginResponse(w, rc, shortResp)
		return
	}

	if rc.IsModelList && rc.Route.Kind == routeKindExternal && hasConcreteModelRoutes(rc.Route) {
		s.serveSyntheticModelList(w, rc, stack)
		return
	}

	prefix := strings.TrimSpace(r.Header.Get(headerPrefixHash))
	if prefix != "" {
		prefix = rc.Route.Workspace + ":" + rc.Route.Key + ":" + prefix
	}

	candidates, modelExists := chooseCandidates(s.State.RuntimeTable(), rc.Route, rc.Model, prefix)
	if !modelExists {
		writePluginResponse(w, rc, errorResponse(http.StatusNotFound, "model_not_found", "", fmt.Sprintf("model %q is not configured on route", rc.Model)))
		return
	}

	if len(candidates) == 0 {
		writePluginResponse(w, rc, errorResponse(http.StatusBadGateway, "bad_gateway", "", "no upstream configured for route"))
		return
	}

	s.runPassthroughAttempt(w, r, rc, stack, candidates[0].upstream, prefix)
}

func (s *Server) runPassthroughAttempt(
	w http.ResponseWriter,
	r *http.Request,
	rc *RequestContext,
	stack *hookStack,
	up state.Upstream,
	prefix string,
) {
	attemptCtx, cancelAttempt := context.WithCancel(rc.Context)
	defer cancelAttempt()

	targetURL := joinUpstreamPath(up.URL, rc.Route.SubPath, r.URL.RawQuery)

	upReq, err := http.NewRequestWithContext(attemptCtx, r.Method, targetURL, rc.RequestBody)
	if err != nil {
		writePluginResponse(w, rc, errorResponse(http.StatusBadRequest, "bad_request", "", err.Error()))
		return
	}

	upReq.ContentLength = rc.RequestBodyLength
	copyHeaders(upReq.Header, r.Header)
	stripUntrustedClientHeaders(upReq.Header)
	upReq.Header.Set(headerRequestID, rc.RequestID)

	if prefix != "" {
		upReq.Header.Set(headerPrefixHash, prefix)
	}

	setUpstreamAuth(upReq.Header, up)

	ac := &AttemptContext{
		Request:         rc,
		Context:         attemptCtx,
		Cancel:          cancelAttempt,
		ID:              rc.RequestID + "-pass-1",
		Upstream:        up,
		TargetModel:     rc.Model,
		UpstreamRequest: upReq,
		TrustedUsage:    up.IsTrustedUsage(rc.Route.Kind),
		StartedAt:       time.Now(),
	}

	release := s.State.RuntimeTable().AcquireInFlight(up.ID)
	defer release()

	if shortResp := stack.attemptStart(ac); shortResp != nil {
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, shortResp)
		ac.StatusCode = rc.ResponseStatus
		stack.attemptDone(ac)

		return
	}

	resp, err := s.Client.Do(upReq)
	if err != nil {
		ac.Err = err
		ac.StatusCode = http.StatusBadGateway
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, errorResponse(http.StatusBadGateway, "bad_gateway", "", err.Error()))
		stack.attemptDone(ac)

		return
	}
	defer resp.Body.Close()

	ac.FirstByteAt = time.Now()
	ac.Response = resp
	ac.StatusCode = resp.StatusCode
	ac.Disposition = AttemptDeliver

	if shortResp := stack.attemptResponse(ac); shortResp != nil {
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, shortResp)
		ac.StatusCode = rc.ResponseStatus
		stack.attemptDone(ac)

		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(headerRequestID, rc.RequestID)

	rc.ResponseStatus = resp.StatusCode
	w.WriteHeader(resp.StatusCode)
	flushResponse(w)

	ac.Err = copyStreamWithFlush(w, resp.Body, cancelAttempt, s.IdleTimeout)
	stack.attemptDone(ac)
}

func (s *Server) serveSyntheticModelList(w http.ResponseWriter, rc *RequestContext, stack *hookStack) {
	now := time.Now()
	resp := syntheticModelResponse(rc.Route, rc.SourceProtocol)

	defer resp.Body.Close()

	ac := &AttemptContext{
		Request:     rc,
		Context:     rc.Context,
		ID:          rc.RequestID + "-models-1",
		Response:    resp,
		StatusCode:  http.StatusOK,
		Disposition: AttemptDeliver,
		Synthetic:   true,
		StartedAt:   now,
		FirstByteAt: now,
	}

	if shortResp := stack.attemptResponse(ac); shortResp != nil {
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, shortResp)
		ac.StatusCode = rc.ResponseStatus
		stack.attemptDone(ac)

		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.Header().Set(headerRequestID, rc.RequestID)

	rc.ResponseStatus = resp.StatusCode
	w.WriteHeader(resp.StatusCode)
	_, _ = io.Copy(w, resp.Body)

	stack.attemptDone(ac)
}

func syntheticModelResponse(route state.Route, sourceProtocol string) *http.Response {
	models := make([]string, 0, len(route.ModelRoutes))

	for model := range route.ModelRoutes {
		if model != "" && model != "*" {
			models = append(models, model)
		}
	}

	sort.Strings(models)

	data := make([]map[string]any, 0, len(models))

	for _, model := range models {
		item := map[string]any{
			"id":           model,
			"object":       "model",
			"type":         "model",
			"created":      0,
			"owned_by":     "external-endpoint",
			"display_name": model,
			"created_at":   "2025-01-01T00:00:00Z",
		}
		data = append(data, item)
	}

	var payload map[string]any

	if sourceProtocol == protocol.ProtocolAnthropicMessages {
		var firstID, lastID any
		if len(models) > 0 {
			firstID = models[0]
			lastID = models[len(models)-1]
		}

		payload = map[string]any{
			"data":     data,
			"has_more": false,
			"first_id": firstID,
			"last_id":  lastID,
		}
	} else {
		payload = map[string]any{
			"object": "list",
			"data":   data,
		}
	}

	body, _ := json.Marshal(payload)
	hdr := make(http.Header)
	hdr.Set("Content-Type", contentTypeJSON)
	hdr.Set("Content-Length", strconv.Itoa(len(body)))

	return &http.Response{
		StatusCode:    http.StatusOK,
		Status:        "200 OK",
		Header:        hdr,
		Body:          io.NopCloser(bytes.NewReader(body)),
		ContentLength: int64(len(body)),
	}
}

func (s *Server) executeTranslated(w http.ResponseWriter, r *http.Request, rc *RequestContext, stack *hookStack) {
	srcCodec, err := protocol.CodecFor(rc.SourceProtocol)
	if err != nil {
		writePluginResponse(w, rc, errorResponse(http.StatusBadRequest, "invalid_protocol", "", err.Error()))
		return
	}

	decodedReq, err := srcCodec.DecodeRequest(http.MaxBytesReader(w, r.Body, s.MaxJSONBodyBytes))
	if err != nil {
		writePluginResponse(w, rc, errorResponse(http.StatusBadRequest, "invalid_request_body", "", err.Error()))
		return
	}

	rc.Model = decodedReq.Model
	rc.CanonicalRequest = &decodedReq

	if shortResp := stack.onRequest(rc); shortResp != nil {
		writePluginResponse(w, rc, shortResp)
		return
	}

	req := *rc.CanonicalRequest

	prefixHash := strings.TrimSpace(r.Header.Get(headerPrefixHash))
	if prefixHash != "" {
		prefixHash = rc.Route.Workspace + ":" + rc.Route.Key + ":" + prefixHash
	} else {
		prefixHash = protocol.PrefixHash(rc.Route.Workspace, rc.Route.Key, req.Model, req)
	}

	candidates, modelExists := chooseCandidates(s.State.RuntimeTable(), rc.Route, req.Model, prefixHash)
	if !modelExists {
		writePluginResponse(w, rc, errorResponse(http.StatusNotFound, "model_not_found", "", fmt.Sprintf("model %q is not configured on route", req.Model)))
		return
	}

	if len(candidates) == 0 {
		writePluginResponse(w, rc, errorResponse(http.StatusBadGateway, "bad_gateway", "", "no candidate upstream available"))
		return
	}

	s.runTranslatedCandidates(w, r, rc, stack, srcCodec, req, candidates, prefixHash)
}

func (s *Server) runTranslatedCandidates(
	w http.ResponseWriter,
	r *http.Request,
	rc *RequestContext,
	stack *hookStack,
	srcCodec protocol.Codec,
	req protocol.CanonicalRequest,
	candidates []candidate,
	prefixHash string,
) {
	mr, ok := rc.Route.ModelRoutes[req.Model]
	if !ok {
		mr = rc.Route.ModelRoutes["*"]
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

			writePluginResponse(w, rc, errorResponse(http.StatusBadRequest, "invalid_request", "", encErr.Error()))

			return
		}

		deliveries++
		attemptID := fmt.Sprintf("%s-try-%d", rc.RequestID, deliveries)

		done, retryStatus, retryErr := s.runTranslatedSingleAttempt(
			w, r, rc, stack, srcCodec, targetCodec, req, c.upstream,
			targetModel, wireBody, attemptID, prefixHash, mr.RetryableConditions, deliveries < maxDeliveries,
		)
		if done {
			return
		}

		if retryErr != nil {
			lastErr = retryErr
		}

		if retryStatus > 0 {
			lastStatus = retryStatus
		}

		if rc.Context.Err() != nil || (retryErr != nil && !isRetryableError(retryErr, mr.RetryableConditions)) {
			break
		}
	}

	if deliveries == 0 && unsupportedErr != nil {
		w.Header().Set("Content-Type", contentTypeJSON)

		rc.ResponseStatus = unsupportedErr.StatusCode()
		w.WriteHeader(unsupportedErr.StatusCode())
		_, _ = w.Write(unsupportedErr.JSON())

		return
	}

	msg := "all candidate upstreams failed before first byte"
	if lastErr != nil {
		msg = lastErr.Error()
	}

	writePluginResponse(w, rc, errorResponse(lastStatus, "upstream_unavailable", "", msg))
}

func (s *Server) runTranslatedSingleAttempt(
	w http.ResponseWriter,
	r *http.Request,
	rc *RequestContext,
	stack *hookStack,
	srcCodec, targetCodec protocol.Codec,
	req protocol.CanonicalRequest,
	up state.Upstream,
	targetModel string,
	wireBody []byte,
	attemptID, prefixHash string,
	retryConditions []string,
	hasMoreDeliveries bool,
) (bool, int, error) {
	attemptCtx, cancelAttempt := context.WithCancel(rc.Context)
	defer cancelAttempt()

	upReq, err := http.NewRequestWithContext(attemptCtx, http.MethodPost, targetCodec.EndpointURL(up.URL), bytes.NewReader(wireBody))
	if err != nil {
		return false, 0, err
	}

	upReq.Header.Set("Content-Type", contentTypeJSON)
	upReq.Header.Set(headerRequestID, rc.RequestID)
	upReq.Header.Set(headerPrefixHash, prefixHash)
	setUpstreamAuth(upReq.Header, up)

	ac := &AttemptContext{
		Request:         rc,
		Context:         attemptCtx,
		Cancel:          cancelAttempt,
		ID:              attemptID,
		Upstream:        up,
		TargetModel:     targetModel,
		UpstreamRequest: upReq,
		TrustedUsage:    up.IsTrustedUsage(rc.Route.Kind),
		Stream:          req.Stream,
		StartedAt:       time.Now(),
	}

	release := s.State.RuntimeTable().AcquireInFlight(up.ID)
	defer release()

	if shortResp := stack.attemptStart(ac); shortResp != nil {
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, shortResp)
		ac.StatusCode = rc.ResponseStatus
		stack.attemptDone(ac)

		return true, 0, nil
	}

	resp, doErr := s.Client.Do(upReq)
	if doErr != nil {
		ac.Err = doErr
		ac.StatusCode = http.StatusBadGateway

		if hasMoreDeliveries && rc.Context.Err() == nil && isRetryableError(doErr, retryConditions) {
			ac.Disposition = AttemptRetry
		} else {
			ac.Disposition = AttemptAbort
		}

		stack.attemptDone(ac)

		return false, 0, doErr
	}
	defer resp.Body.Close()

	if hasMoreDeliveries && isRetryableStatus(resp.StatusCode, retryConditions) {
		ac.Response = resp
		ac.StatusCode = resp.StatusCode
		ac.Disposition = AttemptRetry
		stack.attemptDone(ac)

		return false, resp.StatusCode, nil
	}

	ac.FirstByteAt = time.Now()
	ac.Response = resp
	ac.StatusCode = resp.StatusCode
	ac.Disposition = AttemptDeliver

	if shortResp := stack.attemptResponse(ac); shortResp != nil {
		ac.Disposition = AttemptAbort

		writePluginResponse(w, rc, shortResp)
		ac.StatusCode = rc.ResponseStatus
		stack.attemptDone(ac)

		return true, 0, nil
	}

	s.serveTranslatedResponse(w, r.Context(), resp, cancelAttempt, srcCodec, targetCodec, req, stack, ac)
	stack.attemptDone(ac)

	return true, 0, nil
}

func (s *Server) serveTranslatedResponse(
	w http.ResponseWriter,
	clientCtx context.Context,
	resp *http.Response,
	cancelAttempt context.CancelFunc,
	srcCodec, targetCodec protocol.Codec,
	req protocol.CanonicalRequest,
	stack *hookStack,
	ac *AttemptContext,
) {
	rc := ac.Request

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		copyHeaders(w.Header(), resp.Header)

		rc.ResponseStatus = resp.StatusCode
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)

		return
	}

	if !req.Stream {
		decoded, err := targetCodec.DecodeResponse(resp.Body)
		if err != nil {
			ac.Err = err
			ac.StatusCode = http.StatusBadGateway
			ac.Disposition = AttemptAbort

			writePluginResponse(w, rc, errorResponse(http.StatusBadGateway, "upstream_decode_error", "", err.Error()))

			return
		}

		if shortResp := stack.canonicalResponse(ac, &decoded); shortResp != nil {
			ac.Disposition = AttemptAbort

			writePluginResponse(w, rc, shortResp)
			ac.StatusCode = rc.ResponseStatus

			return
		}

		wire, err := srcCodec.EncodeResponse(decoded, req)
		if err != nil {
			ac.Err = err
			ac.StatusCode = http.StatusBadGateway
			ac.Disposition = AttemptAbort

			writePluginResponse(w, rc, errorResponse(http.StatusBadGateway, "response_encode_error", "", err.Error()))

			return
		}

		w.Header().Set("Content-Type", contentTypeJSON)

		rc.ResponseStatus = http.StatusOK
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(wire)

		return
	}

	s.streamTranslatedSSE(w, clientCtx, resp.Body, cancelAttempt, srcCodec, targetCodec, req, stack, ac)
}

func (s *Server) streamTranslatedSSE(
	w http.ResponseWriter,
	clientCtx context.Context,
	upstreamBody io.Reader,
	cancelAttempt context.CancelFunc,
	srcCodec, targetCodec protocol.Codec,
	req protocol.CanonicalRequest,
	stack *hookStack,
	ac *AttemptContext,
) {
	rc := ac.Request

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")

	rc.ResponseStatus = http.StatusOK
	w.WriteHeader(http.StatusOK)
	flushResponse(w)

	itr := newIdleTimeoutReader(upstreamBody, cancelAttempt, s.IdleTimeout)
	defer itr.Close()

	enc := srcCodec.NewStreamEncoder(flushingWriter{w: w}, req)

	decodeErr := targetCodec.DecodeStream(itr, func(ev protocol.StreamEvent) error {
		if err := stack.streamEvent(ac, &ev); err != nil {
			cancelAttempt()
			return err
		}

		return enc.WriteEvent(ev)
	})

	if streamErr := resolveStreamError(rc, clientCtx, decodeErr); streamErr != nil {
		ac.Err = streamErr
		_ = enc.WriteError(streamErr)

		flushResponse(w)

		return
	}

	_ = enc.Close()

	flushResponse(w)
}

func resolveStreamError(rc *RequestContext, clientCtx context.Context, decodeErr error) error {
	if clientCtx.Err() == nil && rc.Context.Err() != nil {
		if cause := context.Cause(rc.Context); cause != nil && !errors.Is(cause, context.Canceled) {
			return cause
		}
	}

	return decodeErr
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
