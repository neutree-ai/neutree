package logsink

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// fakeS3 answers like an S3 store, deciding by the access key in the signature:
// "good" may do anything, "writeonly" may only put, "nokey" is unknown and
// "badsig" stands for a wrong secret key. Bucket "missing" does not exist.
type fakeS3 struct {
	mu      sync.Mutex
	objects map[string]bool
	puts    []string
}

var credentialRegexp = regexp.MustCompile(`Credential=([^/]+)/`)

func (f *fakeS3) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	f.mu.Lock()
	defer f.mu.Unlock()

	ak := ""
	if m := credentialRegexp.FindStringSubmatch(r.Header.Get("Authorization")); m != nil {
		ak = m[1]
	}

	fail := func(status int, code string) {
		w.WriteHeader(status)
		if r.Method != http.MethodHead {
			fmt.Fprintf(w, `<?xml version="1.0" encoding="UTF-8"?><Error><Code>%s</Code><Message>x</Message></Error>`, code)
		}
	}

	switch ak {
	case "nokey":
		fail(http.StatusForbidden, "InvalidAccessKeyId")
		return
	case "badsig":
		fail(http.StatusForbidden, "SignatureDoesNotMatch")
		return
	}

	bucket, key, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/"), "/")
	if bucket == "missing" {
		fail(http.StatusNotFound, "NoSuchBucket")
		return
	}

	switch {
	case r.Method == http.MethodHead && key == "":
		if ak == "writeonly" {
			fail(http.StatusForbidden, "AccessDenied")
			return
		}
	case r.Method == http.MethodPut:
		f.puts = append(f.puts, key)
		f.objects[key] = true
	case r.Method == http.MethodDelete:
		if ak == "writeonly" {
			fail(http.StatusForbidden, "AccessDenied")
			return
		}

		delete(f.objects, key)
		w.WriteHeader(http.StatusNoContent)
	}
}

func TestTestConnection_S3(t *testing.T) {
	fake := &fakeS3{objects: map[string]bool{}}
	srv := httptest.NewServer(fake)
	defer srv.Close()

	closed := httptest.NewServer(http.NotFoundHandler())
	closed.Close()

	sink := func(endpoint, bucket, ak string) *v1.LogSink {
		return &v1.LogSink{Spec: &v1.LogSinkSpec{Type: v1.LogSinkTypeS3, S3: &v1.S3SinkSpec{
			Endpoint: endpoint, Region: "us-east-1", Bucket: bucket,
			Auth: &v1.S3SinkAuth{AccessKeyID: ak, SecretAccessKey: "sk"},
		}}}
	}

	tests := []struct {
		name    string
		sink    *v1.LogSink
		wantErr string
	}{
		{name: "good", sink: sink(srv.URL, "traces", "good")},
		{name: "write-only keys", sink: sink(srv.URL, "traces", "writeonly")},
		{name: "unknown access key", sink: sink(srv.URL, "traces", "nokey"), wantErr: "does not know access_key_id"},
		{name: "wrong secret key", sink: sink(srv.URL, "traces", "badsig"), wantErr: "secret_access_key does not match"},
		{name: "missing bucket", sink: sink(srv.URL, "missing", "good"), wantErr: `bucket "missing" does not exist (404)`},
		{name: "unreachable", sink: sink(closed.URL, "traces", "good"), wantErr: "cannot reach " + closed.URL},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := TestConnection(context.Background(), tt.sink)
			if tt.wantErr == "" {
				assert.NoError(t, err)
				return
			}

			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}

	fake.mu.Lock()
	defer fake.mu.Unlock()

	// Path style, under the static part of the default prefix.
	require.NotEmpty(t, fake.puts)
	assert.Regexp(t, `^traces/\.connection-test-\d+$`, fake.puts[0])
	// Only the write-only keys leave their test object behind.
	assert.Len(t, fake.objects, 1)
}

func TestTestConnection_S3Timeout(t *testing.T) {
	block := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, _ *http.Request) { <-block }))
	defer srv.Close()
	defer close(block)

	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()

	err := TestConnection(ctx, &v1.LogSink{Spec: &v1.LogSinkSpec{Type: v1.LogSinkTypeS3, S3: &v1.S3SinkSpec{
		Endpoint: srv.URL, Region: "us-east-1", Bucket: "b", Auth: &v1.S3SinkAuth{AccessKeyID: "a", SecretAccessKey: "s"},
	}}})
	require.Error(t, err)
	assert.Equal(t, "cannot reach "+srv.URL+": no answer within 10s", err.Error())
}

func TestDescribeS3Error_Region(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusMovedPermanently)
	}))
	defer srv.Close()

	err := TestConnection(context.Background(), &v1.LogSink{Spec: &v1.LogSinkSpec{Type: v1.LogSinkTypeS3, S3: &v1.S3SinkSpec{
		Endpoint: srv.URL, Region: "eu-west-1", Bucket: "b", Auth: &v1.S3SinkAuth{AccessKeyID: "a", SecretAccessKey: "s"},
	}}})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "check spec.s3.region")
}
