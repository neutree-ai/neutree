package proxy

import (
	"context"
	"errors"
	"io"
	"mime"
	"net"
	"net/http"
	"net/http/httptrace"
	"strings"
	"sync/atomic"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/state"
)

// Timers cancel only this upstream attempt. The handler owns all downstream writes.
func deadline(d time.Duration, cancel context.CancelCauseFunc) func() {
	if d <= 0 {
		return func() {}
	}

	t := time.AfterFunc(d, func() { cancel(context.DeadlineExceeded) })

	return func() { t.Stop() }
}

type idleReader struct {
	io.Reader
	idle      time.Duration
	cancel    context.CancelCauseFunc
	stopFirst func()
}

func (r *idleReader) Read(p []byte) (int, error) {
	stop := deadline(r.idle, r.cancel)
	defer stop()

	n, err := r.Reader.Read(p)
	if n > 0 && r.stopFirst != nil {
		r.stopFirst()
		r.stopFirst = nil
	}

	return n, err
}

func (s *Server) attempt(w http.ResponseWriter, out *http.Request, x *exchange, a *Attempt, base, path string, canRetry bool) (again bool, err error) {
	ctx, cancel := context.WithCancelCause(out.Context())
	defer cancel(nil)

	for _, h := range x.hooks {
		if hook, ok := h.(AttemptHook); ok {
			if err := hook.OnAttemptStart(a); err != nil {
				return false, err
			}

			defer func() { a.Err = err; hook.OnAttemptDone(a) }()
		}
	}

	acquired := deadline(s.Options.Connect, cancel)
	defer acquired()

	start := time.Now()
	if load := a.Target.Load; load != nil {
		load.Arrive(start)

		if err := load.Acquire(ctx); err != nil {
			return canRetry, context.Cause(ctx)
		}

		defer func() { load.Release(); load.Finish(start, time.Now()) }()
	}
	var written atomic.Pointer[time.Time]
	ctx = httptrace.WithClientTrace(ctx, &httptrace.ClientTrace{
		GotConn:      func(httptrace.GotConnInfo) { acquired() },
		WroteRequest: func(httptrace.WroteRequestInfo) { now := time.Now(); written.Store(&now) },
	})

	req, err := upstreamRequest(out, ctx, base, path, x.replay)
	if err != nil {
		return false, err
	}

	resp, err := s.transport.RoundTrip(req)
	if err != nil {
		coolOnDialFailure(a.Target.Load, err)

		if cause := context.Cause(ctx); cause != nil {
			err = cause
		}

		return canRetry, err
	}

	defer resp.Body.Close()

	if canRetry {
		switch resp.StatusCode {
		case 429, 502, 503, 504:
			return true, errors.New(resp.Status)
		}
	}

	if x.managed {
		x.consumer = resp.Header.Get(internal + "Consumer-Id")
		x.upstream = resp.Header.Get(internal + "Selected-Upstream")
		x.upstreamModel = resp.Header.Get(internal + "Upstream-Model")
	}

	cleanHop(resp.Header)
	cleanInternal(resp.Header)

	wait := s.Options.First
	if t := written.Load(); wait > 0 && t != nil {
		wait = max(time.Nanosecond, wait-time.Since(*t))
	}

	first := deadline(wait, cancel)
	defer first()

	body := &idleReader{Reader: resp.Body, idle: s.Options.Idle, cancel: cancel, stopFirst: first}
	media, _, _ := mime.ParseMediaType(resp.Header.Get("Content-Type"))
	stream := x.managed && x.Wire != "" && resp.StatusCode == 200 && media == "text/event-stream"
	err = s.relayResponse(w, x, a, resp, body, first, stream, media)

	if cause := context.Cause(ctx); cause != nil && (err != nil || !x.terminal) {
		err = cause
	}

	if err != nil && x.committed && !stream && out.Context().Err() == nil {
		x.Err = err
		// net/http must not produce a clean chunked EOF after a partial binary/JSON body.
		panic(http.ErrAbortHandler)
	}
	var network net.Error
	retryable := errors.Is(err, io.ErrUnexpectedEOF) || errors.As(err, &network)

	return canRetry && !x.committed && retryable, err
}

func (x *exchange) commit(w http.ResponseWriter, resp *http.Response, a *Attempt, stopFirst func()) error {
	if x.committed {
		return nil
	}

	for _, h := range x.hooks {
		if hook, ok := h.(AttemptHook); ok {
			if err := hook.OnAttemptResponse(a, resp); err != nil {
				return err
			}
		}
	}

	stopFirst()

	for k, v := range resp.Header {
		w.Header()[k] = v
	}

	x.committed = true
	x.Status = resp.StatusCode
	w.WriteHeader(x.Status)

	return nil
}

func (s *Server) relayResponse(w http.ResponseWriter, x *exchange, a *Attempt, resp *http.Response, body *idleReader, stopFirst func(), stream bool, media string) error {
	jsonBody := media == "application/json" || strings.HasPrefix(media, "application/") && strings.HasSuffix(media, "+json")
	textBody := x.Wire != "" || jsonBody || strings.HasPrefix(media, "text/")
	commit := func() error { return x.commit(w, resp, a, stopFirst) }
	deliver := func(data []byte, flush bool) error {
		if err := commit(); err != nil {
			return err
		}

		if x.managed {
			if textBody {
				x.captureResponse(data)
			} else if len(x.responseBody) == 0 {
				x.captureResponse([]byte("[binary response]"))
			}
		}

		if _, err := w.Write(data); err != nil {
			return err
		}

		if flush {
			return http.NewResponseController(w).Flush()
		}

		return nil
	}

	var err error

	switch {
	case stream:
		resp.Header.Del("Content-Length")

		err = x.streamResponse(body, deliver)
	case x.managed && (x.Wire != "" || jsonBody) && resp.StatusCode == 200:
		var b []byte

		b, err = x.jsonResponse(body)
		if err == nil {
			if x.clientCodec != nil {
				resp.Header.Del("Content-Length")
			}

			err = deliver(b, false)
		}

	default:
		var buf [32 << 10]byte

		if s.Options.Mode == L0 {
			err = commit()
			if err == nil {
				err = http.NewResponseController(w).Flush()
			}
		}

		for err == nil {
			var n int

			n, err = body.Read(buf[:])
			if n > 0 {
				if e := deliver(buf[:n], true); e != nil {
					err = e
					break
				}
			}

			if err == io.EOF {
				err = commit()
				break
			}
		}
	}

	return err
}

func coolOnDialFailure(load *state.BackendLoad, err error) {
	var op *net.OpError
	if load != nil && errors.As(err, &op) && op.Op == "dial" {
		load.CoolUntil.Store(time.Now().Add(5 * time.Second).UnixNano())
	}
}
