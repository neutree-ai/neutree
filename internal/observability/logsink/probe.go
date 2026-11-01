package logsink

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

const (
	defaultProbeTimeout = 10 * time.Second
	maxProbeTimeout     = 30 * time.Second
)

// TestConnection checks that the sink's VictoriaLogs answers and accepts its
// credentials, using the same TLS settings Vector will use.
func TestConnection(ctx context.Context, sink *v1.LogSink) error {
	vl := sink.Spec.VictoriaLogs

	client, err := probeClient(vl)
	if err != nil {
		return err
	}

	base := strings.TrimSuffix(vl.URL, "/")

	// /health separates "unreachable" from "credentials rejected". A proxy in
	// front of VictoriaLogs may guard it too, so send the credentials and count
	// 401/403 as reachable; the query below reports the rejection.
	if status, err := probe(ctx, client, base+"/health", vl.Auth); err != nil &&
		status != http.StatusUnauthorized && status != http.StatusForbidden {
		return errors.Wrap(err, "health check failed")
	}

	if _, err := probe(ctx, client, base+"/select/logsql/query?query=*&limit=1", vl.Auth); err != nil {
		return errors.Wrap(err, "authenticated query failed")
	}

	return nil
}

func probeClient(vl *v1.VictoriaLogsSinkSpec) (*http.Client, error) {
	timeout := defaultProbeTimeout
	if vl.TimeoutSeconds > 0 {
		timeout = min(time.Duration(vl.TimeoutSeconds)*time.Second, maxProbeTimeout)
	}

	tlsConfig := &tls.Config{MinVersion: tls.VersionTLS12}

	if vl.CACert != "" {
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM([]byte(vl.CACert)) {
			return nil, errors.New("spec.victorialogs.ca_cert contains no valid PEM certificate")
		}

		tlsConfig.RootCAs = pool
	}

	if vl.InsecureSkipVerify {
		tlsConfig.InsecureSkipVerify = true //nolint:gosec
	}

	transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
	transport.TLSClientConfig = tlsConfig
	// A one-shot probe: pooled connections would only linger after it.
	transport.DisableKeepAlives = true

	return &http.Client{Transport: transport, Timeout: timeout}, nil
}

// probe returns the response status alongside any error; status is 0 when no
// response was received.
func probe(ctx context.Context, client *http.Client, url string, auth *v1.LogSinkAuth) (int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return 0, err
	}

	if auth != nil {
		switch auth.Type {
		case v1.LogSinkAuthTypeBasic:
			req.SetBasicAuth(auth.Username, auth.Password)
		case v1.LogSinkAuthTypeBearer:
			req.Header.Set("Authorization", "Bearer "+auth.Token)
		}
	}

	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 256)) //nolint:errcheck

		return resp.StatusCode, errors.Errorf("status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}

	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<20))

	return resp.StatusCode, nil
}
