package packageimport

import (
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

func TestDownloadPackage(t *testing.T) {
	t.Run("successful download", func(t *testing.T) {
		content := []byte("fake-package-content")
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Length", "20")
			w.WriteHeader(http.StatusOK)
			w.Write(content)
		}))
		defer server.Close()

		destDir := t.TempDir()
		destPath := filepath.Join(destDir, "package.tar.gz")

		err := downloadPackage(context.Background(), server.URL, destPath)
		require.NoError(t, err)

		data, err := os.ReadFile(destPath)
		require.NoError(t, err)
		assert.Equal(t, content, data)
	})

	t.Run("HTTP 404 error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		defer server.Close()

		destDir := t.TempDir()
		destPath := filepath.Join(destDir, "package.tar.gz")

		err := downloadPackage(context.Background(), server.URL, destPath)
		assert.Error(t, err)
		assert.Contains(t, err.Error(), "404")
	})

	t.Run("HTTP 500 error", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			w.WriteHeader(http.StatusInternalServerError)
		}))
		defer server.Close()

		destDir := t.TempDir()
		destPath := filepath.Join(destDir, "package.tar.gz")

		err := downloadPackage(context.Background(), server.URL, destPath)
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

		destDir := t.TempDir()
		destPath := filepath.Join(destDir, "package.tar.gz")

		err := downloadPackage(ctx, server.URL, destPath)
		assert.Error(t, err)
	})
}
