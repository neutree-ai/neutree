// Package leader decides which neutree-core replica runs the background work.
package leader

import (
	"context"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/client-go/tools/leaderelection/resourcelock"
	"k8s.io/klog/v2"
)

// Elector runs lead once this process becomes the leader and blocks until ctx
// is done.
type Elector interface {
	Run(ctx context.Context, lead func(ctx context.Context)) error
}

// AlwaysLeader is used where there is a single replica, such as Docker Compose.
type AlwaysLeader struct{}

func (AlwaysLeader) Run(ctx context.Context, lead func(ctx context.Context)) error {
	go lead(ctx)
	<-ctx.Done()

	return nil
}

// LeaseElector elects a leader through a coordination.k8s.io Lease.
type LeaseElector struct {
	Client        kubernetes.Interface
	LeaseName     string
	Namespace     string
	Identity      string
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration

	// OnLost is called when the lease is lost while ctx is still live.
	// Defaults to exiting the process.
	OnLost func()
}

func (e *LeaseElector) Run(ctx context.Context, lead func(ctx context.Context)) error {
	onLost := e.OnLost
	if onLost == nil {
		onLost = exitOnLost
	}

	elector, err := leaderelection.NewLeaderElector(leaderelection.LeaderElectionConfig{
		Lock: &resourcelock.LeaseLock{
			LeaseMeta: metav1.ObjectMeta{
				Name:      e.LeaseName,
				Namespace: e.Namespace,
			},
			Client:     e.Client.CoordinationV1(),
			LockConfig: resourcelock.ResourceLockConfig{Identity: e.Identity},
		},
		LeaseDuration:   e.LeaseDuration,
		RenewDeadline:   e.RenewDeadline,
		RetryPeriod:     e.RetryPeriod,
		ReleaseOnCancel: true,
		Name:            e.LeaseName,
		Callbacks: leaderelection.LeaderCallbacks{
			OnStartedLeading: func(ctx context.Context) {
				klog.Infof("Acquired leader lease %s/%s as %s", e.Namespace, e.LeaseName, e.Identity)
				lead(ctx)
			},
			OnStoppedLeading: func() {
				// Also called on shutdown and when never elected; only a lease
				// lost while still running is fatal.
				if ctx.Err() != nil {
					return
				}

				onLost()
			},
			OnNewLeader: func(identity string) {
				if identity != e.Identity {
					klog.Infof("Current leader is %s; standing by", identity)
				}
			},
		},
	})
	if err != nil {
		return err
	}

	elector.Run(ctx)

	return nil
}

// exitOnLost exits instead of stopping work in-process, so a former leader
// can never keep writing next to the new one.
func exitOnLost() {
	klog.Error("Lost leader lease, exiting")
	klog.FlushAndExit(klog.ExitFlushTimeout, 1)
}
