package proxy

import (
	"net/http"
	"net/url"
	"strconv"
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/router"
)

const internal = "X-Neutree-Internal-"

func cleanInternal(h http.Header) {
	for k := range h {
		key := strings.ToLower(strings.ReplaceAll(k, "_", "-"))
		if strings.HasPrefix(key, "x-neutree-internal-") || key == "kong-apikey" {
			delete(h, k)
		}
	}
}

func cleanHop(h http.Header) {
	for _, line := range h.Values("Connection") {
		for _, k := range strings.Split(line, ",") {
			h.Del(strings.TrimSpace(k))
		}
	}

	for _, k := range []string{
		"Connection", "Proxy-Connection", "Proxy-Authenticate", "Proxy-Authorization", "Keep-Alive", "Te", "Trailer", "Transfer-Encoding", "Upgrade",
	} {
		h.Del(k)
	}
}

func scrubQuery(raw string) (string, error) {
	if raw == "" {
		return "", nil
	}

	parts := strings.Split(raw, "&")
	out := parts[:0]

	for _, p := range parts {
		key, _, _ := strings.Cut(p, "=")

		k, err := url.QueryUnescape(key)
		if err != nil {
			return "", err
		}

		if strings.Contains(p, ";") {
			return "", url.EscapeError("ambiguous query separator")
		}

		if !strings.EqualFold(k, "kong_apikey") {
			out = append(out, p)
		}
	}

	return strings.Join(out, "&"), nil
}

func readAffinity(h http.Header, s *router.RouteSignals) {
	s.Session = h.Get(internal + "Session")
	if n, err := strconv.ParseUint(h.Get(internal+"Prefix-Hash"), 16, 64); err == nil {
		s.HasPrefix = true
		s.PrefixHash = n
	}

	if value := h.Get(internal + "Block-Hashes"); value != "" && len(value) <= 32*17 {
		for _, k := range strings.Split(value, ",") {
			n, err := strconv.ParseUint(k, 16, 64)
			if err != nil || len(s.BlockHashes) == 32 {
				s.BlockHashes = nil
				break
			}

			s.BlockHashes = append(s.BlockHashes, n)
		}
	}
}

func writeAffinity(h http.Header, s router.RouteSignals) {
	if s.Session != "" {
		h.Set(internal+"Session", s.Session)
	}

	if s.HasPrefix {
		h.Set(internal+"Prefix-Hash", strconv.FormatUint(s.PrefixHash, 16))
	}

	if len(s.BlockHashes) > 0 {
		keys := make([]string, 0, min(32, len(s.BlockHashes)))
		for _, n := range s.BlockHashes[:min(32, len(s.BlockHashes))] {
			keys = append(keys, strconv.FormatUint(n, 16))
		}

		h.Set(internal+"Block-Hashes", strings.Join(keys, ","))
	}
}

// Trace keeps the original client path and query, omitting credential values.
func traceURI(u *url.URL) string {
	path := u.EscapedPath()
	parts := strings.Split(u.RawQuery, "&")
	out := parts[:0]

	for _, part := range parts {
		if part == "" {
			continue
		}

		k, _, _ := strings.Cut(part, "=")

		key, _ := url.QueryUnescape(k)
		switch strings.ToLower(strings.ReplaceAll(key, "-", "_")) {
		case "kong_apikey", "api_key", "apikey", "x_api_key", "authorization", "token", "access_token", "key":
			continue
		}

		out = append(out, part)
	}

	if len(out) > 0 {
		path += "?" + strings.Join(out, "&")
	}

	return path
}
