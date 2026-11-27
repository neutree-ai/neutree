package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/event"
	"github.com/neutree-ai/neutree/internal/icp/proxy"
	"github.com/neutree-ai/neutree/internal/icp/source"
	"github.com/neutree-ai/neutree/internal/icp/state"
)

func run(args []string) error {
	opts, err := proxy.ParseOptions(args)
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, os.Interrupt)
	defer stop()

	background, cancel := context.WithCancel(context.Background())
	defer cancel()

	store := &state.Store{}
	rt := state.NewRuntime(opts.Connections)
	d := event.NewDispatcher(1024)
	app := proxy.NewServer(opts, store, rt)
	sources := []event.Source{source.StaticConfigSource{File: opts.ConfigFile, Config: state.ConfigSnapshot{Kong: opts.Kong}}}

	if opts.Mode == proxy.Cluster {
		pods, err := source.NewInCluster(opts.Namespace, opts.Selector)
		if err != nil {
			return err
		}

		app.PodsReady = pods.Ready.Load
		sources = append(sources, pods)
	}
	// Publication, rather than enqueue, makes the initial configuration ready.
	initial := make(chan error, 1)

	go d.Run(background, func(e event.Event) {
		starting := store.Config() == nil
		err := store.Apply(e)

		if starting {
			select {
			case initial <- err:
			case <-background.Done():
			}
		} else if err != nil {
			slog.Error("rejecting configuration", "error", err)
		}
	}, rt.Apply, rt.Expire)

	for _, src := range sources {
		go func() {
			if err := src.Run(background, d.Emit); err != nil && background.Err() == nil {
				select {
				case initial <- fmt.Errorf("%s: %w", src.Name(), err):
				case <-background.Done():
				}
			}
		}()
	}
	select {
	case <-ctx.Done():
		return nil
	case err := <-initial:
		if err != nil {
			return err
		}
	}

	listener, err := net.Listen("tcp", net.JoinHostPort(opts.Host, strconv.Itoa(opts.Port)))
	if err != nil {
		return err
	}

	if opts.Mode == proxy.Front {
		app.Sink = proxy.NewVectorSink(opts.Vector, 64)
	}

	return serve(ctx, listener, app)
}

func serve(ctx context.Context, listener net.Listener, app *proxy.Server) error {
	reqCtx, cancelRequests := context.WithCancel(context.Background())
	defer cancelRequests()
	// Close can return before an interrupted handler publishes its final trace.
	var handlers sync.RWMutex
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		handlers.RLock()
		defer handlers.RUnlock()
		app.ServeHTTP(w, r)
	})
	server := http.Server{Handler: handler, ReadHeaderTimeout: 10 * time.Second, BaseContext: func(net.Listener) context.Context { return reqCtx }}
	done := make(chan error, 1)

	go func() { done <- server.Serve(listener) }()
	select {
	case err := <-done:
		if !errors.Is(err, http.ErrServerClosed) {
			return err
		}
	case <-ctx.Done():
	}
	app.Draining.Store(true)
	// Keep readiness observable on the listener before Shutdown closes it.
	time.Sleep(time.Second)

	drain, finish := context.WithTimeout(context.Background(), 30*time.Second)
	defer finish()

	if err := server.Shutdown(drain); err != nil {
		cancelRequests()

		_ = server.Close()
	}

	handlers.Lock()
	defer handlers.Unlock()

	if app.Sink != nil {
		flush, end := context.WithTimeout(context.Background(), 5*time.Second)
		defer end()

		if err := app.Sink.Drain(flush); err != nil {
			return fmt.Errorf("draining Vector: %w", err)
		}
	}

	return nil
}

func main() {
	if err := run(os.Args[1:]); err != nil {
		slog.Error("gateway stopped", "error", err)
		os.Exit(1)
	}
}
