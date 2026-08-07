// Package routes holds configuration that belongs to the whole API router
// rather than to any one group of routes.
package routes

import (
	"github.com/gin-gonic/gin"
)

// ConfigureEngine applies the router-level settings the API depends on.
//
// # Why
//
// A model name is one path segment — `/models/:model` — but a public hub names
// its models `org/model`. A client has to percent-encode that slash, and by
// default gin routes on `URL.Path`, which the standard library has already
// decoded: `%2F` is a `/` again by the time the router sees it, so the request
// has one segment too many, matches no route, and is answered with gin's own
// plain-text 404 without ever reaching a handler. Every model on the Hub is
// named that way, so the entire public browsing feature 404s.
//
// UseRawPath routes on `URL.RawPath` instead — the escaped form, in which `%2F`
// is not a separator — and gin unescapes the captured parameters afterwards, so
// handlers still receive `org/model`.
//
// # Scope
//
// This is an engine-wide switch and its consequences are engine-wide, so the
// analysis has to be too. It lives here rather than beside the model routes for
// that reason: the routes that motivated it are not the only ones it reaches.
//
// It changes nothing at all for a URL that contains no percent-escapes, because
// gin only consults RawPath when the escaped and decoded forms of a URL differ;
// for everything else RawPath is empty and the router takes structurally the
// same branch as before. That covers every resource route in the API — workspace,
// cluster, registry and model names are validated to restricted character sets
// (v1.ValidateModelName and its siblings) in which nothing needs escaping — and
// it also covers a proxied path whose only unusual character is one that needs
// no escaping.
//
// Two families of route can carry escapes:
//
//   - The model detail and README routes, which is what this exists for. `%2F`
//     stops being a separator, so the request routes, and the handler receives
//     the decoded name.
//   - The proxy routes, which forward a caller-supplied `*path` to the Ray
//     dashboard, Ray Serve and the Kubernetes API (`internal/routes/proxies`).
//     For a URL that does contain an escape, the captured parameter is now
//     unescaped with url.QueryUnescape rather than being a slice of an
//     already-decoded path.
//
// TestProxyPathEscaping runs each case through the router twice, with this
// setting and without it, and records which ones differ, so this paragraph is a
// measurement rather than an argument. Exactly one case differs: a literal `+`
// in a URL that also carries an escape somewhere, because QueryUnescape reads
// `+` as a space — `/logs/a+b%2Fc` reaches the upstream as `/logs/a%20b/c` where
// it used to be `/logs/a+b/c`. A `+` on its own does not qualify, because
// without an escape there is no RawPath and none of this engages. `%2F`, `%2B`,
// `%25` and double-encoded `%252F` are all unchanged.
//
// # Why not UnescapePathValues = false
//
// Leaving parameters escaped and decoding them in the handlers would avoid the
// `+` divergence, and forwarding the caller's bytes verbatim is arguably the
// more faithful thing for a proxy to do. It does not achieve that here.
// CreateProxyHandlerWithTransport builds its target by interpolating the path
// into a URL string, re-parsing it, and assigning only `req.URL.Path` — never
// `RawPath`. Go derives the wire form from `Path` alone, so an escaped parameter
// would be decoded by that re-parse and the escaping dropped on the way out
// regardless; `%252F` already arrives at the upstream as a plain `/` for exactly
// this reason, with or without this setting. Preserving escaping through the
// proxy is a change to that handler, not to this switch.
func ConfigureEngine(engine *gin.Engine) {
	engine.UseRawPath = true
}
