package model

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sync"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/cmd/neutree-cli/app/cmd/global"
)

// registryServer answers the registry lookup both write commands make first,
// and records every path it is asked for so a test can assert what the command
// did *not* go on to do.
func registryServer(t *testing.T, registryType string) (url string, paths func() []string) {
	t.Helper()

	var (
		mu   sync.Mutex
		seen []string
	)

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		seen = append(seen, r.Method+" "+r.URL.Path)
		mu.Unlock()

		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{"id":1,"metadata":{"name":"public-hugging-face","workspace":"default"},` +
			`"spec":{"type":"` + registryType + `","url":"https://huggingface.co"}}]`))
	}))
	t.Cleanup(server.Close)

	return server.URL, func() []string {
		mu.Lock()
		defer mu.Unlock()

		return append([]string(nil), seen...)
	}
}

func useServer(t *testing.T, url string) {
	t.Helper()

	previous := global.ServerURL
	global.ServerURL = url

	t.Cleanup(func() { global.ServerURL = previous })
}

// A push to a public registry is refused before anything is packed or sent. The
// refusal itself is only half the point: the other half is that a user who
// picked the wrong registry does not pay for a full upload to find out.
func TestPushRefusesAPublicRegistryBeforeUploading(t *testing.T) {
	url, paths := registryServer(t, "hugging-face")
	useServer(t, url)

	modelDir := t.TempDir()
	require.NoError(t, os.WriteFile(filepath.Join(modelDir, "config.json"), []byte(`{}`), 0o600))

	cmd := NewModelCmd()
	cmd.SetArgs([]string{"push", modelDir, "--name", "tinymodel", "--version", "v1", "-r", "public-hugging-face"})

	err := cmd.Execute()

	require.EqualError(t, err,
		`cannot push models to model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)

	for _, path := range paths() {
		require.NotContains(t, path, "/models", "push reached the model endpoint after being refused")
	}
}

// The refusal also has to land before the confirmation prompt: delete reads
// from stdin when --force is absent, so a test that gets as far as the prompt
// hangs or fails on a closed stdin rather than reporting the refusal.
func TestDeleteRefusesAPublicRegistryBeforePrompting(t *testing.T) {
	url, paths := registryServer(t, "hugging-face")
	useServer(t, url)

	cmd := NewModelCmd()
	cmd.SetArgs([]string{"delete", "gpt2:latest", "-r", "public-hugging-face"})

	err := cmd.Execute()

	require.EqualError(t, err,
		`cannot delete models from model registry "public-hugging-face": `+
			`it is a hugging-face registry, which neutree reads from but never writes to`)

	for _, path := range paths() {
		require.NotContains(t, path, "/models", "delete reached the model endpoint after being refused")
	}
}
