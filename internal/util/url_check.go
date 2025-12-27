package util

import (
	"net/url"
	"strings"
)

// IsHTTPOrHTTPSURL returns true if s is a valid URL with scheme "http" or "https" and a non-empty host.
func IsHTTPOrHTTPSURL(s string) bool {
	u, err := url.Parse(strings.TrimSpace(s))
	if err != nil {
		return false
	}

	return (u.Scheme == "http" || u.Scheme == "https") && u.Host != ""
}
