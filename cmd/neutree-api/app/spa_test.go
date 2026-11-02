package app

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
)

func TestSPAFallback(t *testing.T) {
	gin.SetMode(gin.TestMode)

	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "index.html"), []byte("<html>ui</html>"), 0o600); err != nil {
		t.Fatal(err)
	}

	if err := os.MkdirAll(filepath.Join(dir, "assets"), 0o755); err != nil {
		t.Fatal(err)
	}

	if err := os.WriteFile(filepath.Join(dir, "assets", "app.js"), []byte("js"), 0o600); err != nil {
		t.Fatal(err)
	}

	engine := gin.New()
	engine.Use(static.Serve("/", static.LocalFile(dir, true)))
	engine.NoRoute(spaFallback())
	engine.GET("/api/v1/ping", func(c *gin.Context) { c.String(http.StatusOK, "pong") })

	const html = "text/html,application/xhtml+xml"

	cases := []struct {
		name, method, path, accept string
		code                       int
		body, location             string
	}{
		{"index", http.MethodGet, "/", html, http.StatusOK, "<html>ui</html>", ""},
		{"ui route", http.MethodGet, "/organization?tab=teams&team=a", html, http.StatusFound, "", "/#/organization?tab=teams&team=a"},
		{"nested ui route", http.MethodHead, "/user-profiles/show/abc", html, http.StatusFound, "", "/#/user-profiles/show/abc"},
		{"asset", http.MethodGet, "/assets/app.js", "*/*", http.StatusOK, "js", ""},
		{"missing asset", http.MethodGet, "/assets/gone.js", "*/*", http.StatusNotFound, "", ""},
		{"api route", http.MethodGet, "/api/v1/ping", html, http.StatusOK, "pong", ""},
		{"unknown api path", http.MethodGet, "/api/v1/nope", html, http.StatusNotFound, "", ""},
		{"non-GET", http.MethodPost, "/organization", html, http.StatusNotFound, "", ""},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			req := httptest.NewRequest(tc.method, tc.path, nil)
			req.Header.Set("Accept", tc.accept)

			w := httptest.NewRecorder()
			engine.ServeHTTP(w, req)

			if w.Code != tc.code {
				t.Fatalf("HTTP %d; want %d", w.Code, tc.code)
			}

			if tc.body != "" && w.Body.String() != tc.body {
				t.Errorf("body %q; want %q", w.Body.String(), tc.body)
			}

			if got := w.Header().Get("Location"); got != tc.location {
				t.Errorf("Location %q; want %q", got, tc.location)
			}
		})
	}
}
