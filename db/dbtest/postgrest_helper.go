package dbtest

import (
	"fmt"
	"net/http"
	"testing"
	"time"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// TestJWTSecret is the secret db/docker-compose.test.yml hands to both GoTrue
// and PostgREST.
const TestJWTSecret = "test-jwt-secret-32-characters-min"

// GetPostgRESTURL returns the base URL of the PostgREST instance started by
// `make db-test`.
func GetPostgRESTURL() string {
	return getEnvOrDefault("POSTGREST_URL", "http://localhost:6432")
}

// NewTestStorage returns a storage client talking to the test PostgREST, using
// the same service-role token the control plane uses. It blocks until PostgREST
// answers, because the container is started right before the test run and needs
// a moment to build its schema cache.
func NewTestStorage(t *testing.T) storage.Storage {
	t.Helper()

	waitForPostgREST(t)

	s, err := storage.New(storage.Options{
		AccessURL: GetPostgRESTURL(),
		Scheme:    "api",
		JwtSecret: TestJWTSecret,
	})
	if err != nil {
		t.Fatalf("failed to create storage client: %v", err)
	}

	return s
}

func waitForPostgREST(t *testing.T) {
	t.Helper()

	client := &http.Client{Timeout: 2 * time.Second}
	deadline := time.Now().Add(60 * time.Second)

	var lastErr error

	for time.Now().Before(deadline) {
		resp, err := client.Get(GetPostgRESTURL() + "/")
		if err == nil {
			resp.Body.Close()

			if resp.StatusCode < http.StatusInternalServerError {
				return
			}

			// Reached but not serving. Keep the status: it is the difference
			// between "nothing is listening" and "PostgREST cannot reach the
			// database", and only one of those is worth investigating here.
			lastErr = fmt.Errorf("last response was HTTP %d", resp.StatusCode)
		} else {
			lastErr = err
		}

		time.Sleep(time.Second)
	}

	t.Fatalf("PostgREST at %s did not become ready: %v", GetPostgRESTURL(), lastErr)
}
