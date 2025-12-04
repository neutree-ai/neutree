package proxies

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type Dependencies struct {
	Storage          storage.Storage
	StorageAccessURL string
	AuthEndpoint     string
	AuthConfig       middleware.AuthConfig
}

func CreateProxyHandler(targetURL string, path string, modifyRequest func(*http.Request)) gin.HandlerFunc {
	target, err := url.Parse(fmt.Sprintf("%s/%s", targetURL, path))
	if err != nil {
		klog.Errorf("Failed to parse target URL: %v", err)

		return func(c *gin.Context) {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "Failed to create proxy",
			})
		}
	}

	proxy := httputil.NewSingleHostReverseProxy(target)

	originalDirector := proxy.Director
	proxy.Director = func(req *http.Request) {
		originalDirector(req)
		req.URL.Path = target.Path
		req.Host = target.Host

		if modifyRequest != nil {
			modifyRequest(req)
		}
	}

	return func(c *gin.Context) {
		proxy.ServeHTTP(c.Writer, c.Request)
	}
}

func RegisterRayServeProxyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/serve-proxy")

	proxyGroup.Use(middlewares...)
	proxyGroup.Any("/:workspace/:name/*path", handleServeProxy(deps))
}

func RegisterRayDashboardProxyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/ray-dashboard-proxy")

	proxyGroup.Use(middlewares...)
	proxyGroup.Any("/:workspace/:name/*path", handleRayDashboardProxy(deps))
}

func RegisterPostgrestRPCProxyRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/rpc")

	proxyGroup.Use(middlewares...)
	proxyGroup.Any("/:path", handlePostgrestRPCProxy(deps))
}

func handleServeProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "name is required",
			})

			return
		}

		workspace := c.Param("workspace")
		if workspace == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "workspace is required",
			})

			return
		}

		endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(name),
				},
				{
					Column:   "metadata->workspace",
					Operator: "eq",
					Value:    strconv.Quote(workspace),
				},
			},
		})
		if err != nil {
			errS := fmt.Sprintf("Failed to list endpoints: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		if len(endpoints) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "endpoint not found",
			})

			return
		}

		// use internal serve access url
		clusters, err := deps.Storage.ListCluster(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(endpoints[0].Spec.Cluster),
				},
				{
					Column:   "metadata->workspace",
					Operator: "eq",
					Value:    strconv.Quote(endpoints[0].Metadata.Workspace),
				},
			},
		})

		if err != nil {
			errS := fmt.Sprintf("Failed to list clusters: %v", err)
			klog.Errorf(errS)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": errS,
			})

			return
		}

		if len(clusters) == 0 {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "endpoint relate cluster not found",
			})

			return
		}

		if clusters[0].Status.DashboardURL == "" {
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": "cluster dashboard_url not found",
			})

			return
		}

		scheme, host, port, err := util.GetClusterServeAddress(&clusters[0])
		if err != nil {
			errS := fmt.Sprintf("Failed to get cluster serve address: %v", err)
			klog.Errorf(errS)
			c.JSON(http.StatusInternalServerError, gin.H{
				"error": errS,
			})

			return
		}

		serviceURL := fmt.Sprintf("%s://%s:%d/%s/%s", scheme, host, port, workspace, endpoints[0].Metadata.Name)

		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		// TODO: fix this in engine
		if c.Request.Method != "GET" && c.Request.Method != "HEAD" {
			bodyBytes, err := io.ReadAll(c.Request.Body)
			c.Request.Body.Close()

			if err == nil && len(bodyBytes) > 0 {
				var requestBody map[string]interface{}
				if err := json.Unmarshal(bodyBytes, &requestBody); err == nil {
					if _, exists := requestBody["encoding_format"]; exists {
						delete(requestBody, "encoding_format")

						modifiedBodyBytes, err := json.Marshal(requestBody)
						if err == nil {
							c.Request.Body = io.NopCloser(strings.NewReader(string(modifiedBodyBytes)))
							c.Request.ContentLength = int64(len(modifiedBodyBytes))
							c.Request.Header.Set("Content-Length", fmt.Sprintf("%d", len(modifiedBodyBytes)))
						}
					} else {
						c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
					}
				} else {
					c.Request.Body = io.NopCloser(strings.NewReader(string(bodyBytes)))
				}
			}
		}

		proxyHandler := CreateProxyHandler(serviceURL, path, nil)
		proxyHandler(c)
	}
}

func handleRayDashboardProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		name := c.Param("name")
		if name == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "name is required",
			})

			return
		}

		workspace := c.Param("workspace")
		if workspace == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "workspace is required",
			})

			return
		}

		clusters, err := deps.Storage.ListCluster(storage.ListOption{
			Filters: []storage.Filter{
				{
					Column:   "metadata->name",
					Operator: "eq",
					Value:    strconv.Quote(name),
				},
				{
					Column:   "metadata->workspace",
					Operator: "eq",
					Value:    strconv.Quote(workspace),
				},
			},
		})

		if err != nil {
			errS := fmt.Sprintf("Failed to list clusters: %v", err)
			klog.Errorf(errS)

			c.JSON(http.StatusBadRequest, gin.H{
				"error": errS,
			})

			return
		}

		if len(clusters) == 0 {
			c.JSON(http.StatusNotFound, gin.H{
				"error": "cluster not found",
			})

			return
		}

		dashboardURL := clusters[0].Status.DashboardURL
		if dashboardURL == "" {
			c.JSON(http.StatusBadRequest, gin.H{
				"error": "dashboard_url not found",
			})

			return
		}

		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		proxyHandler := CreateProxyHandler(dashboardURL, path, nil)
		proxyHandler(c)
	}
}

func CreatePostgrestAuthModifier(c *gin.Context) func(*http.Request) {
	return func(req *http.Request) {
		if postgrestToken, exists := middleware.GetPostgrestToken(c); exists && postgrestToken != "" {
			req.Header.Set("Authorization", "Bearer "+postgrestToken)
		}
	}
}

func handlePostgrestRPCProxy(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		path := c.Param("path")
		if path != "" && path[0] == '/' {
			path = path[1:]
		}

		path = "rpc/" + path

		proxyHandler := CreateProxyHandler(deps.StorageAccessURL, path, CreatePostgrestAuthModifier(c))
		proxyHandler(c)
	}
}
