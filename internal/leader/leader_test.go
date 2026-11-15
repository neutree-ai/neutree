package leader

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	coordinationv1 "k8s.io/api/coordination/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/kubernetes/fake"
	k8stesting "k8s.io/client-go/testing"
	"k8s.io/utils/ptr"
)

const (
	testLease     = "neutree-core-leader"
	testNamespace = "neutree"
)

func newTestElector(client kubernetes.Interface, identity string, onLost func()) *LeaseElector {
	return &LeaseElector{
		Client:        client,
		LeaseName:     testLease,
		Namespace:     testNamespace,
		Identity:      identity,
		LeaseDuration: time.Second,
		RenewDeadline: 500 * time.Millisecond,
		RetryPeriod:   100 * time.Millisecond,
		OnLost:        onLost,
	}
}

type candidate struct {
	cancel  context.CancelFunc
	done    chan struct{}
	leading chan struct{}
	lost    atomic.Bool
}

func startCandidate(t *testing.T, client kubernetes.Interface, identity string) *candidate {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	c := &candidate{cancel: cancel, done: make(chan struct{}), leading: make(chan struct{})}
	e := newTestElector(client, identity, func() { c.lost.Store(true) })

	go func() {
		defer close(c.done)
		require.NoError(t, e.Run(ctx, func(context.Context) { close(c.leading) }))
	}()

	t.Cleanup(func() {
		cancel()
		<-c.done
	})

	return c
}

func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

func holder(t *testing.T, client kubernetes.Interface) string {
	t.Helper()

	lease, err := client.CoordinationV1().Leases(testNamespace).Get(context.Background(), testLease, metav1.GetOptions{})
	require.NoError(t, err)

	return ptr.Deref(lease.Spec.HolderIdentity, "")
}

func TestLeaseElectorOnlyOneLeads(t *testing.T) {
	client := fake.NewSimpleClientset()

	a := startCandidate(t, client, "core-a")
	require.Eventually(t, func() bool { return isClosed(a.leading) }, 5*time.Second, 20*time.Millisecond)

	b := startCandidate(t, client, "core-b")
	time.Sleep(time.Second)
	require.False(t, isClosed(b.leading), "standby must not run leader work while the lease is held")
	require.Equal(t, "core-a", holder(t, client))
}

func TestLeaseElectorReleasesOnCancelForFastTakeover(t *testing.T) {
	client := fake.NewSimpleClientset()

	a := startCandidate(t, client, "core-a")
	require.Eventually(t, func() bool { return isClosed(a.leading) }, 5*time.Second, 20*time.Millisecond)

	b := startCandidate(t, client, "core-b")

	a.cancel()
	<-a.done
	require.False(t, a.lost.Load(), "a cancelled leader is a shutdown, not a lost lease")

	// Well under the 1s lease duration: takeover relies on the release.
	require.Eventually(t, func() bool { return isClosed(b.leading) }, 600*time.Millisecond, 10*time.Millisecond)
	require.Equal(t, "core-b", holder(t, client))
}

func TestLeaseElectorCallsOnLostWhenRenewFails(t *testing.T) {
	client := fake.NewSimpleClientset()

	// The fake tracker skips resourceVersion checks, so simulate another
	// holder by rejecting renewals the way the API server would. The reactor
	// is installed up front because the chain is not safe to mutate later.
	var taken atomic.Bool
	client.PrependReactor("update", "leases", func(k8stesting.Action) (bool, runtime.Object, error) {
		if !taken.Load() {
			return false, nil, nil
		}

		return true, nil, apierrors.NewConflict(coordinationv1.Resource("leases"), testLease, errors.New("held by another"))
	})

	a := startCandidate(t, client, "core-a")
	require.Eventually(t, func() bool { return isClosed(a.leading) }, 5*time.Second, 20*time.Millisecond)

	taken.Store(true)
	require.Eventually(t, a.lost.Load, 5*time.Second, 20*time.Millisecond)
}

func TestAlwaysLeaderLeadsImmediately(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	leading := make(chan struct{})
	done := make(chan error, 1)

	go func() { done <- AlwaysLeader{}.Run(ctx, func(context.Context) { close(leading) }) }()

	require.Eventually(t, func() bool { return isClosed(leading) }, time.Second, 10*time.Millisecond)
	cancel()
	require.NoError(t, <-done)
}
