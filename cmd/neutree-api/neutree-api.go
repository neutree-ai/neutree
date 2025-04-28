package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/routes/models"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var (
	port             = flag.Int("port", 3000, "API server port")
	host             = flag.String("host", "0.0.0.0", "API server host")
	storageAccessURL = flag.String("storage-access-url", "http://postgrest:6432", "postgrest url")
	storageJwtSecret = flag.String("storage-jwt-secret", "", "storage auth token (JWT_SECRET)")
	ginMode          = flag.String("gin-mode", "release", "gin mode: debug, release, test")
	staticDir        = flag.String("static-dir", "./public", "directory for static files")
	authEndpoint     = flag.String("auth-endpoint", "http://auth:9999", "auth service endpoint")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	serviceToken, err := storage.CreateServiceToken(*storageJwtSecret)
	if err != nil {
		klog.Fatalf("Failed to create service token: %s", err.Error())
	}

	gin.SetMode(*ginMode)

	r := gin.Default()

	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	postgrestProxy := proxies.CreateProxyHandler(*storageAccessURL, "", func(req *http.Request) {
		req.Header.Set("Authorization", "Bearer "+*serviceToken)
	})
	r.Any("/api/v1/*path", postgrestProxy)

	r.Static("/public", *staticDir)

	authProxy := proxies.CreateProxyHandler(*authEndpoint, "", nil)
	r.Any("/api/v1/auth/*path", authProxy)

	s, err := storage.New(storage.Options{
		AccessURL: *storageAccessURL,
		Scheme:    "api",
		JwtSecret: *storageJwtSecret,
	})
	if err != nil {
		klog.Fatalf("Failed to init storage: %s", err.Error())
	}

	models.RegisterRoutes(r, &models.Dependencies{
		Storage: s,
	})

	proxies.RegisterRoutes(r, &proxies.Dependencies{
		Storage: s,
	})

	serverAddr := fmt.Sprintf("%s:%d", *host, *port)
	klog.Infof("Starting API server on %s", serverAddr)
	if err := r.Run(serverAddr); err != nil {
		klog.Fatalf("Failed to start API server: %s", err.Error())
	}
}
