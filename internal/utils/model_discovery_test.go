package utils

import (
	"context"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseModelIDs(t *testing.T) {
	cases := []struct {
		name        string
		body        string
		want        []string
		errContains string
	}{
		{name: "valid response", body: `{"data":[{"id":"model-a"},{"id":"model-b"}]}`, want: []string{"model-a", "model-b"}},
		{name: "preserves order duplicates empty ids and whitespace", body: `{"data":[{"id":"b"},{"id":""},{"id":" b "},{"id":"b"}]}`, want: []string{"b", "", " b ", "b"}},
		{name: "empty data", body: `{"data":[]}`, want: []string{}},
		{name: "invalid json", body: `not json`, errContains: "invalid JSON"},
		{name: "missing data", body: `{"object":"list"}`, errContains: "data"},
		{name: "null data", body: `{"data":null}`, errContains: "data"},
		{name: "object data", body: `{"data":{}}`, errContains: "data"},
		{name: "null response", body: `null`, errContains: "data"},
		{name: "array response", body: `[]`, errContains: "invalid JSON"},
		{name: "numeric id", body: `{"data":[{"id":3}]}`, errContains: "data"},
		{name: "null id", body: `{"data":[{"id":null}]}`, want: []string{""}},
		{name: "missing id", body: `{"data":[{}]}`, want: []string{""}},
		{name: "null model", body: `{"data":[null]}`, want: []string{""}},
		{name: "string model", body: `{"data":["model"]}`, errContains: "data"},
		{name: "trailing json", body: `{"data":[]} {}`, errContains: "invalid JSON"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := parseModelIDs([]byte(tc.body))
			if tc.errContains != "" {
				require.ErrorContains(t, err, tc.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, tc.want, got)
		})
	}
}

type modelDiscoveryTransport func(*http.Request) (*http.Response, error)

func (f modelDiscoveryTransport) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }

type modelDiscoveryBody struct {
	io.Reader
	closed bool
}

func (b *modelDiscoveryBody) Close() error { b.closed = true; return nil }

func TestFetchModelIDs(t *testing.T) {
	cases := []struct {
		name        string
		status      int
		body        string
		errContains string
	}{
		{name: "success", status: http.StatusOK, body: `{"data":[{"id":"alias"}]}`},
		{name: "upstream error preserves body", status: http.StatusUnauthorized, body: `{"error":"invalid api key"}`, errContains: `upstream returned HTTP 401: {"error":"invalid api key"}`},
		{name: "oversized", status: http.StatusOK, body: strings.Repeat("x", (1<<20)+1), errContains: "size limit"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			body := &modelDiscoveryBody{Reader: strings.NewReader(tc.body)}
			request, err := http.NewRequestWithContext(context.Background(), http.MethodGet, "http://upstream/v1/models", nil)
			require.NoError(t, err)
			request.Header.Set("Authorization", "Bearer test")
			client := &http.Client{Transport: modelDiscoveryTransport(func(req *http.Request) (*http.Response, error) {
				assert.Same(t, request, req)
				assert.Equal(t, "Bearer test", req.Header.Get("Authorization"))
				return &http.Response{StatusCode: tc.status, Body: body}, nil
			})}
			got, err := FetchModelIDs(client, request)
			assert.True(t, body.closed)
			if tc.errContains != "" {
				require.ErrorContains(t, err, tc.errContains)
				return
			}
			require.NoError(t, err)
			assert.Equal(t, []string{"alias"}, got)
		})
	}
}

func TestFetchModelIDsConnectionError(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://upstream/v1/models", nil)
	require.NoError(t, err)
	client := &http.Client{Transport: modelDiscoveryTransport(func(req *http.Request) (*http.Response, error) {
		return nil, req.Context().Err()
	})}
	_, err = FetchModelIDs(client, request)
	require.ErrorContains(t, err, "connection failed")
	assert.ErrorIs(t, err, context.Canceled)
}
