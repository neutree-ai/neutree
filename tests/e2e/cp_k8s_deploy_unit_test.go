package e2e

import (
	"net/http"
	"testing"
	"time"
)

func TestNoProxyHTTPClientDisablesProxyFromEnvironment(t *testing.T) {
	t.Setenv("http_proxy", "http://127.0.0.1:7890")
	t.Setenv("https_proxy", "http://127.0.0.1:7890")

	client := newNoProxyHTTPClient(10 * time.Second)
	if client.Timeout != 10*time.Second {
		t.Fatalf("timeout = %s, want %s", client.Timeout, 10*time.Second)
	}

	transport, ok := client.Transport.(*http.Transport)
	if !ok {
		t.Fatalf("transport = %T, want *http.Transport", client.Transport)
	}
	if transport.Proxy != nil {
		t.Fatal("proxy function is configured; NodePort checks must bypass environment proxies")
	}
}
