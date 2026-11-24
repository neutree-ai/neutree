package proxy

import (
	"encoding/json"
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
	headerRequestID  = "X-Neutree-Request-Id"
	headerPrefixHash = "X-Neutree-Prefix-Hash"
	headerUsageToken = "X-Neutree-Usage-Token" //nolint:gosec // HTTP header name, not a credential
	defaultMaxJSON   = 32 << 20                // 32 MiB cap for JSON-decoded translated requests
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
}

type Server struct {
	State            *state.Engine
	Quota            *quota.LeasePool
	Client           *http.Client
	LegacyURL        *url.URL
	IdleTimeout      time.Duration
	MaxJSONBodyBytes int64
	CallbackSecret   []byte
	TokenTTL         time.Duration
	legacy           *httputil.ReverseProxy
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
		Quota:            cfg.Quota,
		Client:           client,
		LegacyURL:        parsedLegacy,
		IdleTimeout:      idle,
		MaxJSONBodyBytes: maxJSON,
		CallbackSecret:   append([]byte(nil), cfg.CallbackSecret...),
		TokenTTL:         tokenTTL,
	}

	if parsedLegacy != nil {
		rp := httputil.NewSingleHostReverseProxy(parsedLegacy)
		rp.FlushInterval = -1
		rp.Transport = NewLegacyTransport(cfg.DialTimeout)
		s.legacy = rp
	}

	return s, nil
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

		writeJSONError(w, http.StatusNotFound, "route_not_found", "no managed route matched request path")

		return
	}

	// 2. Authenticate client API key & check workspace boundary.
	key, ok := state.ExtractAPIKey(r, snap)
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "unauthorized", "missing or invalid API key")
		return
	}

	if key.Workspace != "" && key.Workspace != route.Workspace {
		writeJSONError(w, http.StatusForbidden, "forbidden", "API key cannot access target workspace")
		return
	}

	reqID := strings.TrimSpace(r.Header.Get(headerRequestID))
	if reqID == "" {
		reqID = uuid.NewString()
	}

	w.Header().Set(headerRequestID, reqID)

	// 3. Dual-track dispatch: JSON POST on a known text inference subpath enters translated+retry track;
	// multipart, binary, audio, embeddings, OCR, and custom endpoints enter zero-buffer passthrough track.
	srcProto, isTextProto := protocol.DetectProtocol(route.SubPath)
	if isTextProto && r.Method == http.MethodPost && isJSONContentType(r.Header.Get("Content-Type")) {
		s.forwardTranslated(w, r, snap, route, key, reqID, srcProto)
		return
	}

	s.forwardPassthrough(w, r, snap, route, key, reqID)
}

func isJSONContentType(ct string) bool {
	media := strings.ToLower(strings.TrimSpace(strings.Split(ct, ";")[0]))
	return media == "application/json"
}

func stripUntrustedClientHeaders(h http.Header) {
	h.Del("Authorization")
	h.Del("x-api-key")
	h.Del("kong_apikey")
	h.Del("X-Neutree-Usage")
	h.Del(headerUsageToken)
}

func writeJSONError(w http.ResponseWriter, status int, errType, message string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)

	b, _ := json.Marshal(map[string]any{
		"error": map[string]string{
			"type":    errType,
			"message": message,
		},
	})
	_, _ = w.Write(b)
}
