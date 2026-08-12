// Package routes holds configuration shared by the whole API router.
package routes

import (
	"github.com/gin-gonic/gin"
)

// ConfigureEngine applies the router settings the API depends on.
//
// UseRawPath matches routes against URL.RawPath — the escaped form — so a
// percent-encoded separator stays encoded while the route is chosen. The model
// routes require it: a public registry names its models "org/model", and
// matching against the already-decoded URL.Path gives such a request one path
// segment too many, so it matches no route and never reaches a handler.
// Captured parameters are still unescaped afterwards, so handlers receive
// "org/model".
//
// gin leaves RawPath empty unless a URL actually contains a percent-escape, and
// every resource name in this API is validated to a character set that needs no
// escaping. The routes that see any difference are therefore the model detail
// and README routes, and the proxy routes' caller-supplied *path.
//
// On those proxy routes one case diverges: in a URL that carries an escape, a
// literal '+' in a captured parameter is unescaped to a space, so a proxied
// /logs/a+b%2Fc reaches the upstream as /logs/a%20b/c. TestProxyPathEscaping
// pins this and the cases that do not change.
//
// Setting UnescapePathValues = false instead does not preserve escaping through
// the proxies: CreateProxyHandlerWithTransport re-parses the path into a URL and
// assigns only req.URL.Path, so the escaping is dropped on the way out anyway.
func ConfigureEngine(engine *gin.Engine) {
	engine.UseRawPath = true
}
