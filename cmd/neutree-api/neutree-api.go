package main

import (
	"flag"
	"fmt"
	"net/http"

	"github.com/gin-contrib/static"
	"github.com/gin-gonic/gin"
	"github.com/spf13/pflag"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/routes/admin"
	"github.com/neutree-ai/neutree/internal/routes/models"
	"github.com/neutree-ai/neutree/internal/routes/proxies"
	"github.com/neutree-ai/neutree/internal/routes/system"
	"github.com/neutree-ai/neutree/internal/util"
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
	grafanaURL       = flag.String("grafana-url", "", "grafana url for system info API")
	version          = flag.String("version", "dev", "application version for system info API")
	deployType       = flag.String("deploy-type", "local", "deploy type")
)

func main() {
	klog.InitFlags(nil)
	defer klog.Flush()

	pflag.CommandLine.AddGoFlagSet(flag.CommandLine)
	pflag.Parse()

	gin.SetMode(*ginMode)

	r := gin.Default()

	r.Use(static.Serve("/", static.LocalFile(*staticDir, true)))
	r.GET("/health", func(c *gin.Context) {
		c.JSON(http.StatusOK, gin.H{
			"status": "ok",
		})
	})

	s, err := storage.New(storage.Options{
		AccessURL: *storageAccessURL,
		Scheme:    "api",
		JwtSecret: *storageJwtSecret,
	})
	if err != nil {
		klog.Fatalf("Failed to init storage: %s", err.Error())
	}

	// Configure JWT authentication
	authConfig := middleware.AuthConfig{
		JwtSecret: *storageJwtSecret,
	}

	// Register admin routes FIRST (before catch-all proxy routes)
	admin.RegisterRoutes(r, &admin.Dependencies{
		AuthConfig:   authConfig,
		AuthEndpoint: *authEndpoint,
	})

	// Register routes with authentication configuration
	models.RegisterRoutes(r, &models.Dependencies{
		Storage:    s,
		AuthConfig: authConfig,
	})

	proxies.RegisterRoutes(r, &proxies.Dependencies{
		Storage:          s,
		StorageAccessURL: *storageAccessURL,
		AuthEndpoint:     *authEndpoint,
		AuthConfig:       authConfig,
	})

	grafanaExternalURL, err := util.GetExternalAccessUrl(*deployType, *grafanaURL)
	if err != nil {
		klog.Fatalf("Failed to get grafana external url: %s", err.Error())
	}

	system.RegisterRoutes(r, &system.Dependencies{
		GrafanaURL: grafanaExternalURL,
		Version:    *version,
		AuthConfig: authConfig,
	})

	serverAddr := fmt.Sprintf("%s:%d", *host, *port)
	klog.Infof("Starting API server on %s", serverAddr)

	if err := r.Run(serverAddr); err != nil {
		klog.Fatalf("Failed to start API server: %s", err.Error())
	}
}
