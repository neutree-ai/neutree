package proxy

import (
	"bytes"
	"compress/gzip"
	"encoding/json"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/state"
)

const (
	errTypeNotPermitted  = "not_permitted"
	errCodeModelDenied   = "model_not_permitted"
	errTypeRateLimited   = "rate_limited"
	defaultMaxModelBytes = 8 << 20
)

type RateLimit struct {
	Limit  int64
	Window time.Duration
}

type AccessConfig struct {
	Concurrency       map[string]int
	RateLimits        map[string]RateLimit
	MaxModelListBytes int64
}

type rateWindow struct {
	start time.Time
	count int64
}

type AccessPlugin struct {
	config   AccessConfig
	mu       sync.Mutex
	inflight map[string]int
	windows  map[string]rateWindow
}

func NewAccessPlugin(cfg AccessConfig) *AccessPlugin {
	if cfg.MaxModelListBytes <= 0 {
		cfg.MaxModelListBytes = defaultMaxModelBytes
	}

	return &AccessPlugin{
		config:   cfg,
		inflight: make(map[string]int),
		windows:  make(map[string]rateWindow),
	}
}

func (p *AccessPlugin) Name() string {
	return "access"
}

func (p *AccessPlugin) Begin(_ *RequestContext) RequestHook {
	return &accessSession{plugin: p}
}

func (p *AccessPlugin) InFlight(keyID string) int {
	p.mu.Lock()
	defer p.mu.Unlock()

	return p.inflight[keyID]
}

type accessSession struct {
	plugin   *AccessPlugin
	keyID    string
	occupied bool
}

func (s *accessSession) OnIngress(rc *RequestContext) *Response {
	key, found := lookupAPIKey(rc.Request, rc.Snapshot)
	if !found {
		return errorResponse(http.StatusUnauthorized, "unauthorized", "unauthorized", "missing or invalid API key")
	}

	rc.APIKey = key

	if key.Disabled {
		return errorResponse(http.StatusForbidden, errTypeNotPermitted, "key_disabled", "This API key is disabled")
	}

	if key.Workspace != "" && key.Workspace != rc.Route.Workspace {
		return errorResponse(http.StatusForbidden, errTypeNotPermitted, "forbidden", "API key cannot access target workspace")
	}

	if resp := s.acquireConcurrencyAndRateLimit(key.ID); resp != nil {
		return resp
	}

	if rc.IsModelList {
		if !passthroughEndpointAllowed(key, rc.Route) {
			return errorResponse(http.StatusForbidden, errTypeNotPermitted, errCodeModelDenied, "API key is not permitted to access this endpoint")
		}

		return nil
	}

	if rc.Track == TrackPassthrough && !hasJSONRequestBody(rc.Request) && state.HasModelRestrictions(rc.Snapshot, key) {
		singleModel, ok := singleRouteModel(rc.Route)
		if !ok || !state.Allowed(rc.Snapshot, key, rc.Route.Workspace, singleModel, rc.Route.Kind, rc.Route.Name) {
			return errorResponse(http.StatusForbidden, errTypeNotPermitted, "forbidden", "model-restricted API key cannot access non-JSON passthrough route")
		}

		rc.Model = singleModel
	}

	return nil
}

func (s *accessSession) acquireConcurrencyAndRateLimit(keyID string) *Response {
	if keyID == "" {
		return nil
	}

	p := s.plugin
	if p.config.Concurrency != nil {
		if limit := p.config.Concurrency[keyID]; limit > 0 {
			p.mu.Lock()
			p.inflight[keyID]++
			cur := p.inflight[keyID]

			if cur > limit {
				p.inflight[keyID]--
				if p.inflight[keyID] <= 0 {
					delete(p.inflight, keyID)
				}
				p.mu.Unlock()

				return errorResponse(http.StatusTooManyRequests, errTypeRateLimited, "concurrency_exceeded", "Concurrency limit exceeded for this API key")
			}
			p.mu.Unlock()

			s.keyID = keyID
			s.occupied = true
		}
	}

	if p.config.RateLimits != nil {
		if rl, ok := p.config.RateLimits[keyID]; ok && rl.Limit > 0 && rl.Window > 0 {
			now := time.Now()

			p.mu.Lock()
			w := p.windows[keyID]

			if w.start.IsZero() || now.Sub(w.start) >= rl.Window {
				w = rateWindow{start: now}
			}

			w.count++
			p.windows[keyID] = w
			exceeded := w.count > rl.Limit
			remaining := rl.Window - now.Sub(w.start)
			p.mu.Unlock()

			if exceeded {
				sec := int64(math.Ceil(remaining.Seconds()))
				if sec < 1 {
					sec = 1
				}

				// Note: concurrency slot (if acquired above) is intentionally NOT manually rolled back here;
				// LIFO OnComplete automatically releases s.occupied!
				resp := errorResponse(http.StatusTooManyRequests, errTypeRateLimited, "rate_limit_exceeded", "Request rate limit exceeded for this API key")
				resp.Headers = make(http.Header)
				resp.Headers.Set("Retry-After", strconv.FormatInt(sec, 10))

				return resp
			}
		}
	}

	return nil
}

func (s *accessSession) OnRequest(rc *RequestContext) *Response {
	if rc.IsModelList {
		return nil
	}

	if rc.Model == "" {
		if state.HasModelRestrictions(rc.Snapshot, rc.APIKey) {
			return errorResponse(http.StatusForbidden, errTypeNotPermitted, errCodeModelDenied, "Model not permitted for this API key")
		}

		return nil
	}

	if !state.Allowed(rc.Snapshot, rc.APIKey, rc.Route.Workspace, rc.Model, rc.Route.Kind, rc.Route.Name) {
		return errorResponse(http.StatusForbidden, errTypeNotPermitted, errCodeModelDenied, "Model not permitted for this API key")
	}

	return nil
}

func (s *accessSession) OnComplete(_ *RequestContext) {
	if !s.occupied {
		return
	}

	s.plugin.mu.Lock()
	if s.plugin.inflight[s.keyID] > 0 {
		s.plugin.inflight[s.keyID]--
	}

	if s.plugin.inflight[s.keyID] <= 0 {
		delete(s.plugin.inflight, s.keyID)
	}
	s.plugin.mu.Unlock()
}

func (s *accessSession) OnAttemptStart(_ *AttemptContext) *Response {
	return nil
}

func (s *accessSession) OnAttemptResponse(ac *AttemptContext) *Response {
	rc := ac.Request
	if rc == nil || !rc.IsModelList || ac.Response == nil || ac.StatusCode != http.StatusOK {
		return nil
	}

	if !state.HasModelRestrictions(rc.Snapshot, rc.APIKey) {
		return nil
	}

	resp := ac.Response
	if !strings.Contains(strings.ToLower(resp.Header.Get("Content-Type")), "json") {
		return nil
	}

	body, decodedEncoding, err := readModelListBody(resp, s.plugin.config.MaxModelListBytes)
	if err != nil {
		return errorResponse(http.StatusBadGateway, "bad_gateway", "model_list_read_error", err.Error())
	}

	if int64(len(body)) > s.plugin.config.MaxModelListBytes {
		return errorResponse(http.StatusBadGateway, "bad_gateway", "model_list_too_large", "model list response exceeds maximum size")
	}

	filtered, rewritten := filterModelListJSON(body, rc.Snapshot, rc.APIKey, rc.Route)
	if !rewritten && !decodedEncoding {
		resp.Body = io.NopCloser(bytes.NewReader(body))
		return nil
	}

	resp.Body = io.NopCloser(bytes.NewReader(filtered))
	resp.ContentLength = int64(len(filtered))
	resp.Header.Set("Content-Length", strconv.Itoa(len(filtered)))
	resp.Header.Del("Content-Encoding")
	resp.Header.Del("ETag")
	resp.Header.Del("Content-MD5")

	return nil
}

func readModelListBody(resp *http.Response, maxBytes int64) ([]byte, bool, error) {
	defer resp.Body.Close()

	enc := strings.ToLower(strings.TrimSpace(resp.Header.Get("Content-Encoding")))
	if enc == "gzip" {
		gz, err := gzip.NewReader(resp.Body)
		if err != nil {
			return nil, false, err
		}
		defer gz.Close()

		b, err := io.ReadAll(io.LimitReader(gz, maxBytes+1))

		return b, true, err
	}

	b, err := io.ReadAll(io.LimitReader(resp.Body, maxBytes+1))

	return b, false, err
}

func (s *accessSession) OnAttemptDone(_ *AttemptContext) {}

func lookupAPIKey(r *http.Request, s *state.ConfigSnapshot) (state.APIKey, bool) {
	if s == nil {
		return state.APIKey{}, false
	}

	raw := strings.TrimSpace(r.Header.Get("x-api-key"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("kong_apikey"))
	}

	if raw == "" {
		auth := r.Header.Get("Authorization")
		if len(auth) > 7 && strings.EqualFold(auth[:7], "bearer ") {
			raw = strings.TrimSpace(auth[7:])
		}
	}

	if raw == "" {
		return state.APIKey{}, false
	}

	id, ok := s.Secrets[raw]
	if !ok {
		return state.APIKey{}, false
	}

	key, ok := s.APIKeys[id]

	return key, ok
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

func filterModelListJSON(raw []byte, snap *state.ConfigSnapshot, key state.APIKey, route state.Route) ([]byte, bool) {
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		return raw, false
	}

	obj, _ := doc["object"].(string)
	_, hasMorePresent := doc["has_more"]

	if obj != "list" && !hasMorePresent {
		return raw, false
	}

	data, ok := doc["data"].([]any)
	if !ok {
		return raw, false
	}

	visible := make([]any, 0, len(data))

	for _, entry := range data {
		item, isMap := entry.(map[string]any)
		if !isMap {
			continue
		}

		id, _ := item["id"].(string)
		if id != "" && state.Allowed(snap, key, route.Workspace, id, route.Kind, route.Name) {
			visible = append(visible, item)
		}
	}

	doc["data"] = visible
	updateCursorID(doc, "first_id", visible, 0)
	updateCursorID(doc, "last_id", visible, len(visible)-1)

	out, err := json.Marshal(doc)
	if err != nil {
		return raw, false
	}

	return out, true
}

func updateCursorID(doc map[string]any, field string, visible []any, idx int) {
	if _, exists := doc[field]; !exists {
		return
	}

	if len(visible) == 0 || idx < 0 || idx >= len(visible) {
		doc[field] = nil
		return
	}

	if item, ok := visible[idx].(map[string]any); ok {
		doc[field] = item["id"]
	}
}
