package proxies

import (
	"net/http"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
)

func TestStaticNodeRoutesAreReadOnly(t *testing.T) {
	gin.SetMode(gin.TestMode)

	router := gin.New()
	group := router.Group("/api/v1")
	RegisterStaticNodeClusterRoutes(group, nil, &Dependencies{})
	RegisterStaticNodeRoutes(group, nil, &Dependencies{})

	routes := map[string]map[string]struct{}{}
	for _, route := range router.Routes() {
		if routes[route.Path] == nil {
			routes[route.Path] = map[string]struct{}{}
		}
		routes[route.Path][route.Method] = struct{}{}
	}

	for _, path := range []string{"/api/v1/static_node_clusters", "/api/v1/static_nodes"} {
		assert.Contains(t, routes[path], http.MethodGet)
		assert.NotContains(t, routes[path], http.MethodPost)
		assert.NotContains(t, routes[path], http.MethodPatch)
	}
}
