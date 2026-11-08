package gateway

import (
	"errors"
	"net"
	"net/http"
	"time"
)

// kongAdminTimeouts bounds every Kong admin API call. Admin calls are
// single-entity reads/writes or one page of a per-consumer/per-route list, so
// they finish in milliseconds; the bounds only matter when a peer is gone.
type kongAdminTimeouts struct {
	// Request caps one call end to end, including its retry.
	Request time.Duration
	// Dial caps opening a TCP (and TLS) connection.
	Dial time.Duration
	// ResponseHeader caps the wait for headers on one attempt.
	ResponseHeader time.Duration
	// IdleConn caps how long a pooled connection is reused.
	IdleConn time.Duration
}

var defaultKongAdminTimeouts = kongAdminTimeouts{
	Request:        10 * time.Second,
	Dial:           3 * time.Second,
	ResponseHeader: 3 * time.Second,
	IdleConn:       30 * time.Second,
}

// newKongAdminHTTPClient replaces go-kong's default client, whose 60s timeout
// lets one dead pooled connection stall every gateway sync for a minute.
func newKongAdminHTTPClient(t kongAdminTimeouts) *http.Client {
	transport := &http.Transport{
		DialContext:           (&net.Dialer{Timeout: t.Dial, KeepAlive: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout:   t.Dial,
		ResponseHeaderTimeout: t.ResponseHeader,
		IdleConnTimeout:       t.IdleConn,
		MaxIdleConnsPerHost:   2,
	}

	return &http.Client{
		Timeout:   t.Request,
		Transport: &retryIdempotentOnTimeout{next: transport},
	}
}

// retryIdempotentOnTimeout retries a bodiless GET/HEAD/DELETE once, on a fresh
// connection, when the first attempt timed out. Writes are left to the next
// reconcile, which re-reads Kong state before creating anything.
type retryIdempotentOnTimeout struct {
	next *http.Transport
}

func (r *retryIdempotentOnTimeout) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := r.next.RoundTrip(req)
	if err == nil || !isRetryableKongAdminRequest(req) || !isTimeout(err) || req.Context().Err() != nil {
		return resp, err
	}

	// Other pooled connections may point at the same dead peer.
	r.next.CloseIdleConnections()

	return r.next.RoundTrip(req)
}

func isRetryableKongAdminRequest(req *http.Request) bool {
	switch req.Method {
	case http.MethodGet, http.MethodHead, http.MethodDelete:
		return req.Body == nil || req.Body == http.NoBody
	default:
		return false
	}
}

func isTimeout(err error) bool {
	var netErr net.Error

	return errors.As(err, &netErr) && netErr.Timeout()
}
