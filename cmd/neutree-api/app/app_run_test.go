package app

import (
	"context"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/neutree-ai/neutree/cmd/neutree-api/app/config"
)

const (
	testHost            = "127.0.0.1"
	testStartTimeout    = 10 * time.Second
	testShutdownTimeout = 10 * time.Second
)

func newTestAPIApp(t *testing.T, port int) *App {
	t.Helper()

	return NewApp(&config.APIConfig{
		GinEngine:    gin.New(),
		ServerConfig: &config.ServerConfig{Host: testHost, Port: port},
	})
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
	app := newTestAPIApp(t, port)

	cancel, done := startTestApp(t, app, addr)
	cancel()

	waitRunReturned(t, done, testShutdownTimeout)
	waitPortRebindable(t, addr, 5*time.Second)
}

func TestAppRunRepeatedStartStopReleasesListener(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))

	for range 2 {
		app := newTestAPIApp(t, port)

		cancel, done := startTestApp(t, app, addr)
		cancel()

		waitRunReturned(t, done, testShutdownTimeout)
		waitPortRebindable(t, addr, 5*time.Second)
	}
}

func TestAppRunServesRequestsBeforeCancel(t *testing.T) {
	port := freePort(t)
	addr := net.JoinHostPort(testHost, strconv.Itoa(port))
	app := newTestAPIApp(t, port)
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
