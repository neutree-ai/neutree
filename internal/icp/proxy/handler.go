package proxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"

	"github.com/neutree-ai/neutree/internal/icp/protocol"
	"github.com/neutree-ai/neutree/internal/icp/router"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

type Server struct {
	Options   Options
	Store     *state.Store
	Runtime   *state.RuntimeTable
	Pickers   router.Registry
	Codecs    map[protocol.ID]protocol.Codec
	Sink      UsageSink
	Plugins   []Plugin
	PodsReady func() bool
	Draining  atomic.Bool
	transport *http.Transport
}

func NewServer(o Options, s *state.Store, t *state.RuntimeTable) *Server {
	dialer := net.Dialer{Timeout: o.Dial, KeepAlive: 30 * time.Second}

	return &Server{
		Options: o, Store: s, Runtime: t,
		Pickers: router.NewRegistry(), Codecs: make(map[protocol.ID]protocol.Codec),
		transport: &http.Transport{
			DialContext: dialer.DialContext, ResponseHeaderTimeout: o.First, DisableCompression: true,
			MaxIdleConnsPerHost: 100, IdleConnTimeout: 90 * time.Second,
		},
	}
}

const errorType = "error"

type failure struct {
	status    int
	code, msg string
}

func (f *failure) Error() string { return f.msg }
func bad(err error) error        { return &failure{400, "invalid_request_error", err.Error()} }
func (x *exchange) fail(w http.ResponseWriter, err error) {
	x.Err = err
	if x.committed {
		return
	}

	f := &failure{502, "upstream_error", "upstream request failed"}
	var typed *failure

	switch {
	case errors.As(err, &typed):
		f = typed
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		f = &failure{504, "upstream_timeout", "upstream timed out"}
	case errors.Is(err, router.ErrNoBackend):
		f = &failure{503, "no_backend", err.Error()}
	}

	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("X-Request-Id", x.ID)
	w.WriteHeader(f.status)
	x.Status = f.status
	detail := map[string]string{"type": f.code, "code": f.code, "message": f.msg}

	body := Record{errorType: detail}
	if x.Wire == protocol.AnthropicMessages {
		body["type"] = errorType
	}

	b, _ := json.Marshal(body)
	x.captureResponse(b)
	_, _ = w.Write(b)
}

func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path == "/health" {
		if s.Draining.Load() || s.Store.Config() == nil || s.PodsReady != nil && !s.PodsReady() {
			http.Error(w, "not ready", 503)
			return
		}

		w.Header().Set("Content-Type", "application/json")

		if _, err := io.WriteString(w, `{"status":"healthy"}`); err != nil {
			panic(http.ErrAbortHandler)
		}

		return
	}

	if s.Options.Mode == Cluster && r.URL.Path == "/metrics" {
		s.metrics(w)
		return
	}

	if s.Store.Config() == nil {
		http.Error(w, "not ready", 503)
		return
	}
	// Clone the URL and headers so only the outgoing hop is rewritten.
	out := r.Clone(r.Context())
	x := exchange{RequestContext: RequestContext{Request: out, Config: s.Store.Config(), ID: uuid.NewString()}, started: time.Now()}

	if s.Options.Mode == Cluster {
		readAffinity(out.Header, &x.Signals)
	}

	cleanHop(out.Header)
	cleanInternal(out.Header)
	var err error

	out.URL.RawQuery, err = scrubQuery(out.URL.RawQuery)
	if err != nil {
		x.fail(w, bad(err))
		return
	}

	defer func() {
		for i := len(x.hooks) - 1; i >= 0; i-- {
			x.hooks[i].OnComplete(&x.RequestContext)
		}

		if x.managed && s.Sink != nil {
			s.Sink.Enqueue(x.record())
		}
	}()

	for _, plugin := range s.Plugins {
		if plugin == nil {
			continue
		}

		h := plugin(&x.RequestContext)
		if h == nil {
			continue
		}

		if err = h.OnIngress(&x.RequestContext); err != nil {
			x.fail(w, err)
			return
		}

		x.hooks = append(x.hooks, h)
	}

	s.routeRequest(w, out, &x, r.URL.EscapedPath())
}

func (s *Server) routeRequest(w http.ResponseWriter, out *http.Request, x *exchange, path string) {
	var err error
	suffix := path
	routeKey := ""

	if s.Options.Mode == Cluster {
		parts := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 3)
		if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
			http.NotFound(w, out)
			return
		}

		x.Signals.Endpoint, err = url.PathUnescape(parts[0] + "/" + parts[1])
		if err != nil {
			x.fail(w, bad(err))
			return
		}

		suffix = "/"
		if len(parts) == 3 {
			suffix += parts[2]
		}

		if suffix == "/health" {
			if len(s.Runtime.View(x.Signals.Endpoint, time.Now()).Pods) == 0 {
				http.NotFound(w, out)
				return
			}

			if _, err := io.WriteString(w, `{"status":"healthy"}`); err != nil {
				panic(http.ErrAbortHandler)
			}

			return
		}

		if out.Method == "GET" && strings.TrimSuffix(suffix, "/") == "/v1/models" {
			s.models(w, out, x.Signals.Endpoint)
			return
		}
	} else if s.Options.Mode == Front {
		p := strings.SplitN(strings.TrimPrefix(path, "/"), "/", 5)
		if len(p) == 5 && p[0] == "workspace" && (p[2] == "endpoint" || p[2] == "external-endpoint") {
			p[1], err = url.PathUnescape(p[1])
			if err != nil {
				x.fail(w, bad(err))
				return
			}

			p[3], err = url.PathUnescape(p[3])
			if err != nil {
				x.fail(w, bad(err))
				return
			}

			routeKey = "/" + strings.Join(p[:4], "/")
			suffix = "/" + p[4]
			x.Signals.Endpoint = p[1] + "/" + p[3]
		}
	}

	s.forward(w, out, x, path, suffix, routeKey)
}

func (s *Server) forward(w http.ResponseWriter, out *http.Request, x *exchange, path, suffix, routeKey string) {
	var view state.RuntimeView
	passthrough := false

	if s.Options.Mode == Cluster {
		view = s.Runtime.View(x.Signals.Endpoint, time.Now())
		if len(view.Pods) == 0 {
			x.fail(w, router.ErrNoBackend)
			return
		}

		passthrough = view.Pods[0].Passthrough
		path = suffix
	}

	if s.Options.Mode != L0 {
		if err := s.prepare(x, out, suffix, routeKey, passthrough); err != nil {
			x.fail(w, err)
			return
		}
	}

	if out.Body != nil {
		defer out.Body.Close()
	}

	for _, h := range x.hooks {
		if err := h.OnRequest(&x.RequestContext); err != nil {
			x.fail(w, err)
			return
		}
	}

	if x.managed {
		out.Header.Set("X-Request-Id", x.ID)
		out.Header.Set(internal+"Unpacked", "1")
		out.Header.Set(internal+"Model", x.model)

		stream := "0"
		if x.Envelope.Stream {
			stream = "1"
		}

		out.Header.Set(internal+"Stream", stream)
		writeAffinity(out.Header, x.Signals)

		if s.Options.Token != "" {
			out.Header.Set(internal+"Token", s.Options.Token)
		}

		out.Header.Set("Accept-Encoding", "identity")
	}

	if x.clientCodec != nil {
		path = strings.TrimSuffix(path, suffix) + "/v1/chat/completions"

		out.Header.Del("Anthropic-Version")
		out.Header.Del("Anthropic-Beta")
	}

	if err := s.dispatch(w, out, x, path, view); err != nil {
		x.fail(w, err)
	}
}

func (s *Server) dispatch(w http.ResponseWriter, out *http.Request, x *exchange, path string, view state.RuntimeView) error {
	base := x.Config.Kong
	var picker router.EndpointPicker

	if len(view.Pods) > 0 {
		picker = s.Pickers.Resolve(view.Pods[0].RoutingLogic)
	}

	canReplay := x.replay != nil || out.Body == nil || out.Body == http.NoBody

	for n := 1; ; n++ {
		a := Attempt{Request: &x.RequestContext, Number: n}

		if picker != nil {
			target, err := picker.Pick(x.Signals, view)
			if err != nil {
				return err
			}

			a.Target = target
			base = target.URL
		}

		canRetry := picker != nil && len(view.Pods) > 1 && a.Number == 1 && canReplay

		again, err := s.attempt(w, out, x, &a, base, path, canRetry)
		if !again || out.Context().Err() != nil {
			return err
		}

		x.Signals.ExcludeUID = a.Target.UID
	}
}

func (s *Server) prepare(x *exchange, out *http.Request, path, key string, passthrough bool) error {
	fromFirstHop := x.Signals.HasPrefix || x.Signals.Session != "" || len(x.Signals.BlockHashes) > 0

	if x.Signals.Session == "" {
		x.Signals.Session = out.Header.Get(s.Options.SessionKey)
		if x.Signals.Session == "" {
			x.Signals.Session = out.Header.Get("X-Session-ID")
		}
	}

	if passthrough || out.Method != "POST" {
		return nil
	}

	media, params, _ := mime.ParseMediaType(out.Header.Get("Content-Type"))
	x.Wire = protocol.Detect(path)
	front := s.Options.Mode == Front

	if media == "multipart/form-data" && front && key != "" {
		x.managed = true

		model, body, ct, err := multipartBody(out.Body, params["boundary"])
		if err != nil {
			return bad(err)
		}

		out.Body = body
		out.ContentLength = -1
		out.Header.Set("Content-Type", ct)
		out.Header.Del("Content-Length")

		x.model = model
		x.requestBody = []byte("[multipart/form-data]")

		return nil
	}

	if x.Wire == "" || front && key == "" {
		return nil
	}

	x.managed = front
	if front && (x.Wire == protocol.AnthropicMessages || x.Wire == protocol.OpenAIResponses) {
		x.clientCodec = s.Codecs[x.Wire]
		x.upstreamCodec = s.Codecs[protocol.OpenAIChat]

		if x.clientCodec == nil || x.upstreamCodec == nil {
			return &failure{501, "protocol_conversion_unavailable", "cross-protocol conversion is unavailable"}
		}
	}

	if media != "application/json" {
		if front {
			return bad(errors.New("expected application/json"))
		}

		return nil
	}

	return s.prepareJSON(x, out, key, fromFirstHop)
}

func (s *Server) prepareJSON(x *exchange, out *http.Request, key string, fromFirstHop bool) error {
	front := s.Options.Mode == Front
	// On trusted first-hop requests the router already has the key. Retain a bounded
	// replay body only for JSON inference, never for an opaque upload.
	raw, err := readBounded(out.Body, 16<<20)
	if err != nil {
		return err
	}

	x.replay = raw

	if !front && fromFirstHop {
		return nil
	}

	if front {
		x.requestBody = raw[:min(len(raw), traceLimit)]
	}

	env, err := protocol.Inspect(raw)
	if err != nil {
		if !front {
			return nil
		}

		return bad(err)
	}

	x.Envelope = env
	x.model = env.Model
	x.Signals.HasPrefix = env.HasPrefix
	x.Signals.PrefixHash = env.PrefixHash
	x.Signals.BlockHashes = env.BlockHashes

	if x.Signals.Session == "" {
		if v := env.Fields[s.Options.SessionKey]; v != nil {
			_ = json.Unmarshal(v, &x.Signals.Session)
		}
	}

	if x.Signals.Session == "" {
		x.Signals.Session = env.Session
	}

	if !front {
		return nil
	}

	if !validModel(x.model) {
		return bad(errors.New("invalid model field"))
	}

	defaults := x.Config.Routes[key].ModelDefaults[x.model]

	if x.clientCodec != nil {
		request, err := x.clientCodec.DecodeRequest(env)
		if err != nil {
			return bad(err)
		}

		x.replay, err = x.upstreamCodec.EncodeRequest(request, protocol.EncodeOpts{Model: x.model, IncludeUsage: env.Stream, Defaults: defaults})
		if err != nil {
			return bad(err)
		}

		return nil
	}

	x.replay, err = protocol.RewriteSame(x.Wire, env, defaults)
	if err != nil {
		return bad(err)
	}

	return nil
}

func readBounded(r io.Reader, n int64) ([]byte, error) {
	b, err := io.ReadAll(io.LimitReader(r, n+1))
	if int64(len(b)) > n {
		return nil, &failure{413, "size_limit", "JSON body exceeds limit"}
	}

	return b, err
}

func upstreamRequest(out *http.Request, ctx context.Context, base, path string, replay []byte) (*http.Request, error) {
	u, err := url.Parse(strings.TrimSuffix(base, "/") + path)
	if err != nil {
		return nil, err
	}

	req := out.Clone(ctx)
	u.RawQuery = out.URL.RawQuery
	req.URL = u
	req.Host = u.Host
	req.RequestURI = ""

	if replay != nil {
		req.Body = io.NopCloser(bytes.NewReader(replay))
		req.ContentLength = int64(len(replay))
		req.Header.Del("Content-Length")
	}

	return req, nil
}
