package app

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/cmd/neutree-core/app/config"
	"github.com/neutree-ai/neutree/internal/observability/manager"
	"github.com/neutree-ai/neutree/pkg/storage"
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
	store := mockstorage.NewMockStorage(t)
	store.On("ListReleaseInfo").Return([]v1.ReleaseInfo{}, nil).Once()
	store.On("ListClusterProfile", storage.ListOption{}).Return([]v1.ClusterProfile{}, nil).Once()
	store.On("CreateReleaseInfo", mock.Anything).Return(nil).Once()
	store.On("CreateClusterProfile", mock.Anything).Return(nil).Times(3)

	return &config.CoreConfig{
		GinEngine:               gin.New(),
		ObsCollectConfigManager: obsCollectConfigManagerStub{},
		Storage:                 store,
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

func TestAppRunShutdownTimeoutReleasesListenerWithInFlightRequest(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestCoreApp(t, port)

	started := make(chan struct{})
	release := make(chan struct{})
	app.config.GinEngine.GET("/slow", func(c *gin.Context) {
		close(started)
		<-release
		c.String(http.StatusOK, "done")
	})

	cancel, done := startTestApp(t, app, addr)

	go func() {
		resp, err := http.Get("http://" + addr + "/slow")
		if err == nil {
			_ = resp.Body.Close()
		}
	}()

	<-started // the request is now in-flight inside the handler

	cancel()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("App.Run() error = nil, want shutdown timeout error")
		}
	case <-time.After(testShutdownTimeout + 2*time.Second):
		t.Fatalf("App.Run() did not return within %v", testShutdownTimeout+2*time.Second)
	}

	close(release)
	waitPortRebindable(t, addr, 5*time.Second)
}

func TestAppRunReturnsErrorWhenPortOccupied(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestCoreApp(t, port)

	listener, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("occupy port: %v", err)
	}
	defer listener.Close()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	select {
	case err := <-done:
		if err == nil {
			t.Fatal("App.Run() error = nil, want bind error")
		}
	case <-time.After(testStartTimeout):
		t.Fatalf("App.Run() did not return the bind error within %v", testStartTimeout)
	}
}

func TestAppRunPreCancelledContextReleasesListener(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestCoreApp(t, port)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	done := make(chan error, 1)
	go func() {
		done <- app.Run(ctx)
	}()

	waitRunReturned(t, done, testShutdownTimeout)
	waitPortRebindable(t, addr, 5*time.Second)
}

func TestAppRunWithInjectedPluginShutsDownOnCancel(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	cfg := newTestCoreConfig(t, port)
	injected := internalTestPlugin{}
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
