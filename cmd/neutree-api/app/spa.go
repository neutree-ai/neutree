package app

import (
	"net/http"
	"strings"

	"github.com/gin-gonic/gin"
)

// spaFallback redirects a path-style UI link (/organization?tab=teams) to the
// UI's hash route (/#/organization?tab=teams). API paths and non-HTML requests
// keep the 404.
func spaFallback() gin.HandlerFunc {
	return func(c *gin.Context) {
		r := c.Request
		if (r.Method != http.MethodGet && r.Method != http.MethodHead) ||
			strings.HasPrefix(r.URL.Path, "/api/") ||
			!strings.Contains(r.Header.Get("Accept"), "text/html") {
			c.Status(http.StatusNotFound)
			return
		}

		target := "/#" + r.URL.EscapedPath()
		if r.URL.RawQuery != "" {
			target += "?" + r.URL.RawQuery
		}

		c.Redirect(http.StatusFound, target)
	}
}
