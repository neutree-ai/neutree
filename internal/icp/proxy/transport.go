package proxy

import (
	"context"
	"errors"
	"io"
	"net"
	"net/http"
	"sync/atomic"
	"time"

	"golang.org/x/net/http2"
)

var ErrIdleTimeout = errors.New("upstream stream idle timeout")

// NewHTTPClient builds an HTTP/1.1 + HTTP/2 client with dial and first-byte header timeouts,
// without imposing a fixed total-request deadline that would kill long-running SSE or audio streams.
func NewHTTPClient(dialTimeout, firstByteTimeout time.Duration) *http.Client {
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	if firstByteTimeout <= 0 {
		firstByteTimeout = 30 * time.Second
	}

	transport := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		DialContext:           (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout:   dialTimeout,
		ResponseHeaderTimeout: firstByteTimeout,
		MaxIdleConns:          200,
		MaxIdleConnsPerHost:   64,
		IdleConnTimeout:       90 * time.Second,
	}
	_ = http2.ConfigureTransport(transport)

	return &http.Client{Transport: transport}
}

// NewLegacyTransport returns a streaming HTTP transport without a short ResponseHeaderTimeout
// so long-running synchronous requests falling back to legacy Kong (3600s default) are not prematurely aborted.
func NewLegacyTransport(dialTimeout time.Duration) *http.Transport {
	if dialTimeout <= 0 {
		dialTimeout = 10 * time.Second
	}

	transport := &http.Transport{
		Proxy:               http.ProxyFromEnvironment,
		DialContext:         (&net.Dialer{Timeout: dialTimeout, KeepAlive: 30 * time.Second}).DialContext,
		TLSHandshakeTimeout: dialTimeout,
		MaxIdleConns:        200,
		MaxIdleConnsPerHost: 64,
		IdleConnTimeout:     90 * time.Second,
	}
	_ = http2.ConfigureTransport(transport)

	return transport
}

// idleTimeoutReader enforces a per-chunk read idle timeout on the same goroutine
// by cancelling the upstream request context if no bytes arrive within timeout.
type idleTimeoutReader struct {
	reader   io.Reader
	cancel   context.CancelFunc
	timeout  time.Duration
	timer    *time.Timer
	timedOut atomic.Bool
}

func newIdleTimeoutReader(r io.Reader, cancel context.CancelFunc, timeout time.Duration) *idleTimeoutReader {
	if timeout <= 0 {
		timeout = 2 * time.Minute
	}

	itr := &idleTimeoutReader{
		reader:  r,
		cancel:  cancel,
		timeout: timeout,
	}
	itr.timer = time.AfterFunc(timeout, func() {
		itr.timedOut.Store(true)

		if itr.cancel != nil {
			itr.cancel()
		}
	})
	itr.timer.Stop()

	return itr
}

func (r *idleTimeoutReader) Read(p []byte) (int, error) {
	r.timer.Reset(r.timeout)
	n, err := r.reader.Read(p)
	r.timer.Stop()

	if r.timedOut.Load() {
		return n, ErrIdleTimeout
	}

	return n, err
}

func (r *idleTimeoutReader) Close() {
	if r.timer != nil {
		r.timer.Stop()
	}
}

type flushingWriter struct {
	w http.ResponseWriter
}

func (fw flushingWriter) Write(p []byte) (int, error) {
	n, err := fw.w.Write(p)
	if n > 0 {
		flushResponse(fw.w)
	}

	return n, err
}

func flushResponse(w http.ResponseWriter) {
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
		return
	}

	_ = http.NewResponseController(w).Flush()
}

func copyStreamWithFlush(w http.ResponseWriter, r io.Reader, cancel context.CancelFunc, idle time.Duration) error {
	itr := newIdleTimeoutReader(r, cancel, idle)
	defer itr.Close()

	var buf [32 * 1024]byte

	for {
		n, err := itr.Read(buf[:])
		if n > 0 {
			if _, wErr := w.Write(buf[:n]); wErr != nil {
				return wErr
			}

			flushResponse(w)
		}

		if err == io.EOF {
			return nil
		}

		if err != nil {
			return err
		}
	}
}
