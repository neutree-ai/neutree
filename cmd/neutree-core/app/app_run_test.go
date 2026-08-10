package app

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/internal/accelerator/plugin"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	mockstorage "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const (
	testHost            = "127.0.0.1"
	testStartTimeout    = 10 * time.Second
	testShutdownTimeout = 10 * time.Second
)

// obsCollectConfigManagerStub satisfies ObsCollectConfigManager without
// wiring the real observability collector, which is not under test.
type obsCollectConfigManagerStub struct{}

func (obsCollectConfigManagerStub) GetMetricsCollectConfigManager() manager.MetricsCollectConfigManager {
	return nil
}

func (obsCollectConfigManagerStub) Start(context.Context) {}

func newTestCoreConfig(t *testing.T, port int) *config.CoreConfig {
	t.Helper()

	return &config.CoreConfig{
		GinEngine:               gin.New(),
		ObsCollectConfigManager: obsCollectConfigManagerStub{},
		Storage:                 mockstorage.NewMockStorage(t),
		ServerConfig:            &config.ServerConfig{Host: testHost, Port: port},
	}
}

func newTestCoreApp(t *testing.T, port int) *App {
	t.Helper()

	builder := NewBuilder().WithConfig(newTestCoreConfig(t, port))
	builder.controllerInits = map[string]ControllerFactory{}

	app, err := builder.Build()
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}

	return app
}

func freePort(t *testing.T) int {
	t.Helper()

	listener, err := net.Listen("tcp", net.JoinHostPort(testHost, "0"))
	if err != nil {
		t.Fatalf("allocate probe port: %v", err)
	}

	port := listener.Addr().(*net.TCPAddr).Port
	if err := listener.Close(); err != nil {
		t.Fatalf("close probe listener: %v", err)
	}

	return port
}

func startTestApp(t *testing.T, app *App, addr string) (context.CancelFunc, <-chan error) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	waitForListener(t, addr, testStartTimeout)

	return cancel, done
}

func waitForListener(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		conn, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			_ = conn.Close()

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("listener %s not connectable within %v: %v", addr, timeout, err)
		}

		time.Sleep(25 * time.Millisecond)
	}
}

func waitRunReturned(t *testing.T, done <-chan error, timeout time.Duration) {
	t.Helper()

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("App.Run() error = %v", err)
		}
	case <-time.After(timeout):
		t.Fatalf("App.Run() did not return within %v", timeout)
	}
}

func waitPortRebindable(t *testing.T, addr string, timeout time.Duration) {
	t.Helper()

	deadline := time.Now().Add(timeout)
	for {
		listener, err := net.Listen("tcp", addr)
		if err == nil {
			_ = listener.Close()

			return
		}

		if time.Now().After(deadline) {
			t.Fatalf("port %s still occupied within %v: %v", addr, timeout, err)
		}

		time.Sleep(25 * time.Millisecond)
	}
}

func TestAppRunReleasesListenerOnContextCancel(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestCoreApp(t, port)

	cancel, done := startTestApp(t, app, addr)
	cancel()

	waitRunReturned(t, done, testShutdownTimeout)
	waitPortRebindable(t, addr, 5*time.Second)
}

func TestAppRunRepeatedStartStopReleasesListener(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))

	for range 2 {
		app := newTestCoreApp(t, port)

		cancel, done := startTestApp(t, app, addr)
		cancel()

		waitRunReturned(t, done, testShutdownTimeout)
		waitPortRebindable(t, addr, 5*time.Second)
	}
}

func TestAppRunServesRequestsBeforeCancel(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestCoreApp(t, port)
	app.config.GinEngine.GET("/healthz", func(c *gin.Context) {
		c.String(http.StatusOK, "ok")
	})

	cancel, done := startTestApp(t, app, addr)
	defer cancel()

	resp, err := http.Get("http://" + addr + "/healthz")
	if err != nil {
		t.Fatalf("GET /healthz: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET /healthz status = %d, want %d", resp.StatusCode, http.StatusOK)
	}

	cancel()

	waitRunReturned(t, done, testShutdownTimeout)
	waitPortRebindable(t, addr, 5*time.Second)
}

func TestAppRunWithInjectedPluginShutsDownOnCancel(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	cfg := newTestCoreConfig(t, port)
	injected := internalTestPlugin{
		AcceleratorPlugin: plugin.NewAcceleratorRestPlugin("injected-test", "http://plugin.example"),
	}
	builder := NewBuilder().WithConfig(cfg).WithAcceleratorPlugins(injected)
	builder.controllerInits = map[string]ControllerFactory{}

	app, err := builder.Build()
	if err != nil {
		t.Fatalf("Build() with injected plugin error = %v", err)
	}

	cancel, done := startTestApp(t, app, addr)
	cancel()

	waitRunReturned(t, done, testShutdownTimeout)
	waitPortRebindable(t, addr, 5*time.Second)
}
