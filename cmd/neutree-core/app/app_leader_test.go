package app

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/controllers"
	mockgateway "github.com/neutree-ai/neutree/internal/gateway/mocks"
)

// fakeElector leads only when lead is true, standing in for a replica that
// won or lost the election.
type fakeElector struct{ lead bool }

func (f fakeElector) Run(ctx context.Context, lead func(context.Context)) error {
	if f.lead {
		go lead(ctx)
	}

	<-ctx.Done()

	return nil
}

type startRecorder struct{ started chan struct{} }

func (r *startRecorder) Start(ctx context.Context) {
	close(r.started)
	<-ctx.Done()
}

func (r *startRecorder) Name() string { return "recorder" }

func newLeaderTestApp(t *testing.T, elector fakeElector) (*App, *startRecorder) {
	t.Helper()

	cfg := newTestCoreConfig(t, freePort(t))
	cfg.LeaderElector = elector

	gw := mockgateway.NewMockGateway(t)
	if elector.lead {
		gw.EXPECT().Init().Return(nil).Maybe()
	}

	cfg.Gateway = gw

	rec := &startRecorder{started: make(chan struct{})}

	return NewApp(cfg, map[string]controllers.Controller{"recorder": rec}), rec
}

func TestAppRunStartsBackgroundWorkOnlyOnLeader(t *testing.T) {
	cases := []struct {
		name string
		lead bool
	}{
		{name: "leader", lead: true},
		{name: "standby", lead: false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			app, rec := newLeaderTestApp(t, fakeElector{lead: tc.lead})

			ctx, cancel := context.WithCancel(context.Background())
			done := make(chan error, 1)
			go func() { done <- app.Run(ctx) }()

			select {
			case <-rec.started:
				require.True(t, tc.lead, "standby started a controller")
			case <-time.After(500 * time.Millisecond):
				require.False(t, tc.lead, "leader did not start controllers")
			}

			cancel()
			require.NoError(t, <-done)
		})
	}
}

func TestAppRunRetriesGatewayInitWithoutExiting(t *testing.T) {
	gatewayInitBackoffMin = 10 * time.Millisecond
	t.Cleanup(func() { gatewayInitBackoffMin = 2 * time.Second })

	cfg := newTestCoreConfig(t, freePort(t))
	cfg.LeaderElector = fakeElector{lead: true}

	var calls atomic.Int32

	gw := mockgateway.NewMockGateway(t)
	gw.EXPECT().Init().RunAndReturn(func() error {
		if calls.Add(1) < 3 {
			return errors.New("kong down")
		}

		return nil
	}).Maybe()
	cfg.Gateway = gw

	rec := &startRecorder{started: make(chan struct{})}
	app := NewApp(cfg, map[string]controllers.Controller{"recorder": rec})

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Run(ctx) }()

	select {
	case <-rec.started:
	case <-time.After(time.Second):
		t.Fatal("controllers did not start while gateway init was failing")
	}

	require.Eventually(t, func() bool { return calls.Load() >= 3 }, time.Second, 10*time.Millisecond,
		"gateway init was not retried until it succeeded")

	select {
	case err := <-done:
		t.Fatalf("App.Run() returned while gateway init was retrying: %v", err)
	default:
	}

	cancel()
	require.NoError(t, <-done)
}
