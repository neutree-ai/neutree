package packageimport

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestFormatBytes(t *testing.T) {
	tests := []struct {
		name     string
		bytes    int64
		expected string
	}{
		{"zero bytes", 0, "0 B"},
		{"small bytes", 500, "500 B"},
		{"one KiB", 1024, "1.0 KiB"},
		{"1.5 MiB", 1024 * 1024 * 3 / 2, "1.5 MiB"},
		{"one GiB", 1024 * 1024 * 1024, "1.0 GiB"},
		{"one TiB", 1024 * 1024 * 1024 * 1024, "1.0 TiB"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := formatBytes(tt.bytes)
			assert.Equal(t, tt.expected, result)
		})
	}
}

// buildTestTarGz creates a minimal tar.gz in memory with a manifest.yaml file.
func buildTestTarGz(t *testing.T) []byte {
	t.Helper()

	var buf bytes.Buffer
	gw := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gw)

	content := []byte(`manifest_version: "1.0"`)
	require.NoError(t, tw.WriteHeader(&tar.Header{
		Name: "manifest.yaml",
		Size: int64(len(content)),
		Mode: 0644,
	}))

	_, err := tw.Write(content)
	require.NoError(t, err)
	require.NoError(t, tw.Close())
	require.NoError(t, gw.Close())

	return buf.Bytes()
}

func TestStreamExtractPackage(t *testing.T) {
	extractor := NewExtractor()

	t.Run("successful stream extract", func(t *testing.T) {
		archive := buildTestTarGz(t)
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
			w.Write(archive)
		}))
		defer server.Close()

		destDir := t.TempDir()

		err := streamExtractPackage(context.Background(), server.URL, extractor, destDir)
		require.NoError(t, err)

		data, err := os.ReadFile(filepath.Join(destDir, "manifest.yaml"))
		require.NoError(t, err)
		assert.Contains(t, string(data), "manifest_version")
	})

	t.Run("HTTP 404 error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		err := streamExtractPackage(context.Background(), server.URL, extractor, t.TempDir())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})

	t.Run("HTTP 500 error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		err := streamExtractPackage(context.Background(), server.URL, extractor, t.TempDir())
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "500")
	})

	t.Run("context cancellation", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusOK)
		}))
		defer server.Close()

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := streamExtractPackage(ctx, server.URL, extractor, t.TempDir())
		assert.Error(t, err)
	})
}
