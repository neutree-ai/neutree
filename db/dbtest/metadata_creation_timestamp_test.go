package dbtest

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// metadata is a composite column, so PostgREST rewrites all of it from whatever
// JSON a PATCH carries and any subfield the payload omits comes back NULL.
// These tests go through raw HTTP rather than the typed storage client for
// exactly that reason: the Go client always sends a whole v1.Metadata, and the
// payload that loses a creation time is one that does not (NEU-717).
func postgrestRequest(t *testing.T, method, path, body string) (int, string) {
	t.Helper()

	token, err := storage.CreateServiceToken(TestJWTSecret)
	if err != nil {
		t.Fatalf("failed to mint a service token: %v", err)
	}

	req, err := http.NewRequest(method, GetPostgRESTURL()+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("failed to build the request: %v", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept-Profile", "api")
	req.Header.Set("Content-Profile", "api")
	req.Header.Set("Authorization", "Bearer "+*token)
	req.Header.Set("Prefer", "return=representation")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("request failed: %v", err)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("failed to read the response: %v", err)
	}

	return resp.StatusCode, string(raw)
}

// endpointCreationTimestamp returns the creation_timestamp of the single row a
// representation response carries, and whether it is non-null.
func endpointCreationTimestamp(t *testing.T, body string) (string, bool) {
	t.Helper()

	var rows []struct {
		Metadata struct {
			CreationTimestamp *string `json:"creation_timestamp"`
		} `json:"metadata"`
	}

	if err := json.Unmarshal([]byte(body), &rows); err != nil {
		t.Fatalf("failed to decode the response %q: %v", body, err)
	}

	if len(rows) != 1 {
		t.Fatalf("expected exactly one row, got %d: %s", len(rows), body)
	}

	if rows[0].Metadata.CreationTimestamp == nil {
		return "", false
	}

	return *rows[0].Metadata.CreationTimestamp, true
}

func createTimestampTestEndpoint(t *testing.T, name string) string {
	t.Helper()

	code, body := postgrestRequest(t, http.MethodPost, "/endpoints", `{
		"api_version": "v1",
		"kind": "Endpoint",
		"metadata": {"name": "`+name+`", "workspace": "default"},
		"spec": {
			"cluster": "c1",
			"model": {"name": "qwen3", "version": "latest"},
			"engine": {"engine": "vllm", "version": "v0.24.0"},
			"replicas": {"num": 1}
		}
	}`)

	if code != http.StatusCreated {
		t.Fatalf("failed to create the endpoint: %d %s", code, body)
	}

	t.Cleanup(func() {
		postgrestRequest(t, http.MethodDelete, "/endpoints?metadata->>name=eq."+name, "")
	})

	created, ok := endpointCreationTimestamp(t, body)
	if !ok {
		t.Fatalf("insert did not stamp a creation timestamp: %s", body)
	}

	return created
}

func TestMetadataCreationTimestampSurvivesAPartialMetadataPatch(t *testing.T) {
	waitForPostgREST(t)

	t.Run("a patch that resends metadata without the creation timestamp", func(t *testing.T) {
		created := createTimestampTestEndpoint(t, "ts-partial")

		// The shape a client produces when it echoes the fields it knows about
		// and drops the ones it does not -- a pause, a relabel, a rename.
		code, body := postgrestRequest(t, http.MethodPatch, "/endpoints?metadata->>name=eq.ts-partial",
			`{"metadata": {"name": "ts-partial", "workspace": "default", "labels": {"paused": "true"}}}`)
		if code != http.StatusOK {
			t.Fatalf("patch failed: %d %s", code, body)
		}

		after, ok := endpointCreationTimestamp(t, body)
		if !ok {
			t.Fatalf("the creation timestamp was erased by the patch: %s", body)
		}

		if after != created {
			t.Fatalf("the creation timestamp changed: created %q, after patch %q", created, after)
		}
	})

	t.Run("a soft delete", func(t *testing.T) {
		created := createTimestampTestEndpoint(t, "ts-soft-delete")

		code, body := postgrestRequest(t, http.MethodPatch, "/endpoints?metadata->>name=eq.ts-soft-delete",
			`{"metadata": {"name": "ts-soft-delete", "workspace": "default", "deletion_timestamp": "2026-08-25T06:29:27Z"}}`)
		if code != http.StatusOK {
			t.Fatalf("patch failed: %d %s", code, body)
		}

		after, ok := endpointCreationTimestamp(t, body)
		if !ok {
			t.Fatalf("the creation timestamp was erased by the soft delete: %s", body)
		}

		if after != created {
			t.Fatalf("the creation timestamp changed: created %q, after patch %q", created, after)
		}
	})

	// The insert trigger ignores a creation timestamp the payload states, so the
	// update trigger has to ignore one too: the column belongs to the database
	// on both paths, or it belongs to it on neither.
	t.Run("a patch that states a creation timestamp", func(t *testing.T) {
		created := createTimestampTestEndpoint(t, "ts-explicit")

		code, body := postgrestRequest(t, http.MethodPatch, "/endpoints?metadata->>name=eq.ts-explicit",
			`{"metadata": {"name": "ts-explicit", "workspace": "default", "creation_timestamp": "2020-01-02T03:04:05Z"}}`)
		if code != http.StatusOK {
			t.Fatalf("patch failed: %d %s", code, body)
		}

		after, ok := endpointCreationTimestamp(t, body)
		if !ok {
			t.Fatalf("the creation timestamp was erased by the patch: %s", body)
		}

		if after != created {
			t.Fatalf("a patch rewrote the creation timestamp: created %q, after patch %q", created, after)
		}
	})
}
