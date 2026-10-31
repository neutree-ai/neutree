package app

import (
	"context"
	"errors"
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

func newLeaderTestApp(t *testing.T, elector fakeElector, initErr error) (*App, *startRecorder) {
	t.Helper()

	cfg := newTestCoreConfig(t, freePort(t))
	cfg.LeaderElector = elector

	gw := mockgateway.NewMockGateway(t)
	if elector.lead {
		gw.EXPECT().Init().Return(initErr).Once()
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
			app, rec := newLeaderTestApp(t, fakeElector{lead: tc.lead}, nil)

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

func TestAppRunFailsWhenLeaderGatewayInitFails(t *testing.T) {
	app, rec := newLeaderTestApp(t, fakeElector{lead: true}, errors.New("kong down"))

	done := make(chan error, 1)
	go func() { done <- app.Run(context.Background()) }()

	select {
	case err := <-done:
		require.ErrorContains(t, err, "kong down")
	case <-time.After(testShutdownTimeout):
		t.Fatal("App.Run() did not return after gateway init failed")
	}

	select {
	case <-rec.started:
		t.Fatal("controllers started although gateway init failed")
	default:
	}
}
