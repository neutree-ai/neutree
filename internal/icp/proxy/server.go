package proxy

import (
	"context"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

const (
	headerRequestID   = "X-Neutree-Request-Id"
	headerPrefixHash  = "X-Neutree-Prefix-Hash"
	headerUsageToken  = "X-Neutree-Usage-Token" //nolint:gosec // HTTP header name, not a credential
	defaultMaxJSON    = 32 << 20                // 32 MiB cap for JSON-decoded requests
	contentTypeJSON   = "application/json"
	routeKindExternal = "external"
)

type Config struct {
	State            *state.Engine
	Quota            *quota.LeasePool
	Client           *http.Client
	LegacyKongURL    string
	DialTimeout      time.Duration
	FirstByteTimeout time.Duration
	IdleTimeout      time.Duration
	MaxJSONBodyBytes int64
	CallbackSecret   []byte
	TokenTTL         time.Duration
	Plugins          []Plugin
}

type Server struct {
	State            *state.Engine
	Client           *http.Client
	LegacyURL        *url.URL
	IdleTimeout      time.Duration
	MaxJSONBodyBytes int64
	legacy           *httputil.ReverseProxy
	plugins          []Plugin
	telemetry        *TelemetryPlugin
}

func NewServer(cfg Config) (*Server, error) {
	var parsedLegacy *url.URL
	var err error

	if cfg.LegacyKongURL != "" {
		parsedLegacy, err = url.Parse(cfg.LegacyKongURL)
		if err != nil {
			return nil, err
		}
	}

	client := cfg.Client
	if client == nil {
		client = NewHTTPClient(cfg.DialTimeout, cfg.FirstByteTimeout)
	}

	idle := cfg.IdleTimeout
	if idle <= 0 {
		idle = 2 * time.Minute
	}

	maxJSON := cfg.MaxJSONBodyBytes
	if maxJSON <= 0 {
		maxJSON = defaultMaxJSON
	}

	tokenTTL := cfg.TokenTTL
	if tokenTTL <= 0 {
		tokenTTL = 10 * time.Minute
	}

	s := &Server{
		State:            cfg.State,
		Client:           client,
		LegacyURL:        parsedLegacy,
		IdleTimeout:      idle,
		MaxJSONBodyBytes: maxJSON,
	}

	if cfg.Plugins == nil {
		s.telemetry = NewTelemetryPlugin()
		s.plugins = []Plugin{
			s.telemetry,
			NewAccessPlugin(AccessConfig{}),
			NewQuotaPlugin(cfg.Quota, cfg.CallbackSecret, tokenTTL),
		}
	} else {
		s.plugins = append([]Plugin(nil), cfg.Plugins...)
		for _, p := range s.plugins {
			if t, ok := p.(*TelemetryPlugin); ok {
				s.telemetry = t
			}
		}
	}

	if parsedLegacy != nil {
		rp := httputil.NewSingleHostReverseProxy(parsedLegacy)
		rp.FlushInterval = -1
		rp.Transport = NewLegacyTransport(cfg.DialTimeout)
		s.legacy = rp
	}

	return s, nil
}

func (s *Server) Telemetry() *TelemetryPlugin {
	return s.telemetry
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	snap := s.State.Snapshot()

	// 1. Route ownership check BEFORE touching r.Body: unmanaged routes zero-buffer stream to LegacyURL.
	route, ok := snap.MatchRoute(r.URL.Path)
	if !ok {
		if s.legacy != nil {
			s.legacy.ServeHTTP(w, r)
			return
		}

		writePluginResponse(w, &RequestContext{}, errorResponse(http.StatusNotFound, "route_not_found", "", "no managed route matched request path"))

		return
	}

	reqID := strings.TrimSpace(r.Header.Get(headerRequestID))
	if reqID == "" {
		reqID = uuid.NewString()
	}

	w.Header().Set(headerRequestID, reqID)

	isModelList := isModelListRequest(r.Method, route.SubPath)
	srcProto, isTextProto := protocol.DetectProtocol(route.SubPath)

	if isModelList && strings.HasPrefix(strings.TrimRight(route.SubPath, "/"), "/anthropic/") {
		srcProto = protocol.ProtocolAnthropicMessages
	}

	track := TrackPassthrough
	if isTextProto && r.Method == http.MethodPost && isJSONContentType(r.Header.Get("Content-Type")) {
		track = TrackTranslated
	}

	ctx, cancel := context.WithCancelCause(r.Context())
	defer cancel(nil)

	rc := &RequestContext{
		Context:           ctx,
		Cancel:            cancel,
		Writer:            w,
		Request:           r,
		Snapshot:          snap,
		Route:             route,
		RequestID:         reqID,
		Track:             track,
		SourceProtocol:    srcProto,
		IsModelList:       isModelList,
		RequestBody:       r.Body,
		RequestBodyLength: r.ContentLength,
	}

	stack, shortResp := s.beginHooks(rc)
	defer stack.complete(rc)

	if shortResp != nil {
		writePluginResponse(w, rc, shortResp)
		return
	}

	if track == TrackTranslated {
		s.executeTranslated(w, r, rc, stack)
		return
	}

	s.executePassthrough(w, r, rc, stack)
}

func isJSONContentType(ct string) bool {
	media := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	return media == contentTypeJSON
}

func hasJSONRequestBody(r *http.Request) bool {
	return r.Method != http.MethodGet && r.Method != http.MethodHead && isJSONContentType(r.Header.Get("Content-Type"))
}

func stripUntrustedClientHeaders(h http.Header) {
	h.Del("Authorization")
	h.Del("x-api-key")
	h.Del("kong_apikey")
	h.Del("X-Neutree-Usage")
	h.Del(headerUsageToken)
	h.Del("Accept-Encoding")
}
