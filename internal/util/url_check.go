package util

import (
	"net/url"
	"strings"
)

const (
	SchemeHTTP  = "http"
	SchemeHTTPS = "https"
)

// IsHTTPOrHTTPSURL returns true if s is a valid URL with scheme "http" or "https" and a non-empty host.
func IsHTTPOrHTTPSURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}

	return (u.Scheme == SchemeHTTP || u.Scheme == SchemeHTTPS) && u.Host != ""
}
