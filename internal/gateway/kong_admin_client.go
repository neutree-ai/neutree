package gateway

import (
	"net/http"
	"time"
)

const (
	kongAdminRequestTimeout        = 10 * time.Second
	kongAdminResponseHeaderTimeout = 3 * time.Second
)

// newKongAdminHTTPClient replaces go-kong's default client, whose 60s timeout
// lets one dead pooled connection stall every gateway sync for a minute. A
// call that times out fails fast and is retried by the next reconcile.
func newKongAdminHTTPClient(requestTimeout, responseHeaderTimeout time.Duration) *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
	// go-kong's default client never uses an environment proxy; keep it that way.
	transport.Proxy = nil
	transport.ResponseHeaderTimeout = responseHeaderTimeout
	transport.IdleConnTimeout = 30 * time.Second

	return &http.Client{Timeout: requestTimeout, Transport: transport}
}
