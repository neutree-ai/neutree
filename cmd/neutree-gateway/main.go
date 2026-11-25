package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"golang.org/x/net/http2"
	"golang.org/x/net/http2/h2c"

	"github.com/neutree-ai/neutree/internal/icp/proxy"
	"github.com/neutree-ai/neutree/internal/icp/quota"
	"github.com/neutree-ai/neutree/internal/icp/state"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type gatewayConfig struct {
	listen, adminListen, adminToken            string
	legacyURL, managedRoutes, seedFile         string
	postgrestURL, jwtSecret, callbackSecret    string
	syncInterval, tokenTTL, leaseTTL           time.Duration
	leaseStep                                  int64
	dialTimeout, firstByteTimeout, idleTimeout time.Duration
	guardrailBlock, guardrailRedact            string
}

type gatewayApp struct {
	engine         *state.Engine
	leases         *quota.LeasePool
	proxy          *proxy.Server
	callbackSecret []byte
	adminToken     string
}

func main() {
	if err := run(parseFlags()); err != nil {
		log.Print(err)
		os.Exit(1)
	}
}

func run(cfg gatewayConfig) error {
	app, err := newGatewayApp(cfg)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	go app.engine.Run()
	defer app.engine.Close()

	if err = app.initialize(ctx, cfg); err != nil {
		return err
	}

	dataServer := &http.Server{
		Addr:              cfg.listen,
		Handler:           h2c.NewHandler(app.buildDataMux(), &http2.Server{}),
		ReadHeaderTimeout: 10 * time.Second,
	}

	go serve(dataServer)

	var adminServer *http.Server
	if cfg.adminListen != "off" && cfg.adminListen != "" {
		adminServer = &http.Server{
			Addr:              cfg.adminListen,
			Handler:           app.buildAdminMux(),
			ReadHeaderTimeout: 10 * time.Second,
		}

		go serve(adminServer)
	}

	<-ctx.Done()

	shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	_ = dataServer.Shutdown(shutdownCtx)

	if adminServer != nil {
		_ = adminServer.Shutdown(shutdownCtx)
	}

	return nil
}

func parseFlags() gatewayConfig {
	var cfg gatewayConfig

	flag.StringVar(&cfg.listen, "listen", ":8080", "data-plane listen address")
	flag.StringVar(&cfg.adminListen, "admin-listen", "127.0.0.1:8081", "admin listen address, or off")
	flag.StringVar(&cfg.adminToken, "admin-token", "", "optional admin bearer token")
	flag.StringVar(&cfg.legacyURL, "legacy-kong-url", "", "legacy gateway URL for unmanaged route fallback")
	flag.StringVar(&cfg.managedRoutes, "managed-routes", "*", "comma-separated managed route keys, *, or none")
	flag.StringVar(&cfg.seedFile, "seed-file", "", "optional JSON ConfigEvent seed path")
	flag.StringVar(&cfg.postgrestURL, "postgrest-url", "", "PostgREST access URL")
	flag.StringVar(&cfg.jwtSecret, "storage-jwt-secret", "", "PostgREST JWT signing secret")
	flag.DurationVar(&cfg.syncInterval, "sync-interval", 15*time.Second, "PostgREST sync interval")
	flag.StringVar(&cfg.callbackSecret, "callback-secret", "", "HMAC secret for async usage callbacks")
	flag.DurationVar(&cfg.tokenTTL, "token-ttl", 10*time.Minute, "signed usage token TTL")
	flag.Int64Var(&cfg.leaseStep, "lease-step", 64, "quota lease step size")
	flag.DurationVar(&cfg.leaseTTL, "lease-ttl", 5*time.Minute, "quota lease TTL")
	flag.DurationVar(&cfg.dialTimeout, "dial-timeout", 10*time.Second, "upstream dial timeout")
	flag.DurationVar(&cfg.firstByteTimeout, "first-byte-timeout", 30*time.Second, "upstream response header timeout")
	flag.DurationVar(&cfg.idleTimeout, "idle-timeout", 2*time.Minute, "upstream stream idle timeout")
	flag.StringVar(&cfg.guardrailBlock, "guardrail-block", "", "comma-separated guardrail block terms")
	flag.StringVar(&cfg.guardrailRedact, "guardrail-redact", "", "comma-separated guardrail redact terms")
	flag.Parse()

	return cfg
}

func newGatewayApp(cfg gatewayConfig) (*gatewayApp, error) {
	managed := parseManagedRoutes(cfg.managedRoutes)
	hasLegacy := cfg.legacyURL != ""

	leases := quota.NewLeasePool(cfg.leaseStep, cfg.leaseTTL)
	engine := state.NewEngine(nil, leases, 128)
	_ = engine.Publish(state.Event{
		Kind:   state.EventConfig,
		Config: &state.ConfigEvent{LegacyFallback: &hasLegacy, ManagedRoutes: managed},
	})

	engine.Drain()

	secret := []byte(cfg.callbackSecret)
	plugins := []proxy.Plugin{
		proxy.NewTelemetryPlugin(),
		proxy.NewAccessPlugin(proxy.AccessConfig{}),
	}

	if cfg.guardrailBlock != "" || cfg.guardrailRedact != "" {
		plugins = append(plugins, proxy.NewGuardrailPlugin(
			strings.Split(cfg.guardrailBlock, ","),
			strings.Split(cfg.guardrailRedact, ","),
		))
	}

	plugins = append(plugins, proxy.NewQuotaPlugin(leases, secret, cfg.tokenTTL))

	p, err := proxy.NewServer(proxy.Config{
		State:            engine,
		Quota:            leases,
		LegacyKongURL:    cfg.legacyURL,
		DialTimeout:      cfg.dialTimeout,
		FirstByteTimeout: cfg.firstByteTimeout,
		IdleTimeout:      cfg.idleTimeout,
		CallbackSecret:   secret,
		TokenTTL:         cfg.tokenTTL,
		Plugins:          plugins,
	})
	if err != nil {
		return nil, err
	}

	return &gatewayApp{
		engine:         engine,
		leases:         leases,
		proxy:          p,
		callbackSecret: secret,
		adminToken:     cfg.adminToken,
	}, nil
}

func (a *gatewayApp) initialize(ctx context.Context, cfg gatewayConfig) error {
	if cfg.seedFile != "" {
		loaded, err := state.LoadSeed(cfg.seedFile)
		if err != nil {
			return err
		}

		if err = a.engine.PublishAndWait(ctx, state.Event{Kind: state.EventConfig, Config: loaded}); err != nil {
			return err
		}
	}

	if cfg.postgrestURL == "" {
		return nil
	}

	store, err := storage.New(storage.Options{
		AccessURL: cfg.postgrestURL,
		Scheme:    "api",
		JwtSecret: cfg.jwtSecret,
	})
	if err != nil {
		return err
	}

	syncer := &state.PostgRESTSyncer{Storage: store, Engine: a.engine}
	if err = syncer.SyncAndWait(ctx); err != nil {
		return err
	}

	go a.syncLoop(ctx, syncer, cfg.syncInterval)

	return nil
}

func (a *gatewayApp) syncLoop(ctx context.Context, syncer *state.PostgRESTSyncer, interval time.Duration) {
	if interval <= 0 {
		interval = 15 * time.Second
	}

	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			if err := syncer.SyncAndWait(ctx); err != nil && ctx.Err() == nil {
				log.Printf("postgrest sync error: %v", err)
			}
		case <-ctx.Done():
			return
		}
	}
}

func parseManagedRoutes(raw string) map[string]bool {
	result := make(map[string]bool)

	for _, item := range strings.Split(raw, ",") {
		item = strings.TrimSpace(item)
		if item != "" && item != "none" {
			result[item] = true
		}
	}

	return result
}

func serve(server *http.Server) {
	if err := server.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
		log.Printf("gateway listener %s: %v", server.Addr, err)
	}
}

func writeJSON(w http.ResponseWriter, status int, value any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(value)
}

func health(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet && r.Method != http.MethodHead {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]string{"error": "method_not_allowed"})
		return
	}

	writeJSON(w, http.StatusOK, map[string]string{"status": "ok"})
}
