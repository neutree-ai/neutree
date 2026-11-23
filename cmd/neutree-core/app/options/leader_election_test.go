package options

import (
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
	"k8s.io/client-go/rest"

	"github.com/neutree-ai/neutree/internal/leader"
)

func inCluster() (*rest.Config, error) { return &rest.Config{Host: "https://127.0.0.1:6443"}, nil }

func notInCluster() (*rest.Config, error) { return nil, rest.ErrNotInCluster }

func TestLeaderElectionElector(t *testing.T) {
	t.Setenv("POD_NAME", "neutree-core-abc")

	t.Run("disabled runs as leader", func(t *testing.T) {
		o := NewLeaderElectionOptions()
		o.Enabled = false

		e, err := o.Elector(inCluster)
		require.NoError(t, err)
		require.IsType(t, leader.AlwaysLeader{}, e)
	})

	t.Run("outside kubernetes runs as leader", func(t *testing.T) {
		e, err := NewLeaderElectionOptions().Elector(notInCluster)
		require.NoError(t, err)
		require.IsType(t, leader.AlwaysLeader{}, e)
	})

	t.Run("in-cluster config error is fatal", func(t *testing.T) {
		_, err := NewLeaderElectionOptions().Elector(func() (*rest.Config, error) {
			return nil, errors.New("bad token")
		})
		require.ErrorContains(t, err, "bad token")
	})

	t.Run("in cluster uses a lease", func(t *testing.T) {
		t.Setenv("POD_NAMESPACE", "neutree-ns")

		o := NewLeaderElectionOptions()
		o.LeaseName = "neutree-core"

		e, err := o.Elector(inCluster)
		require.NoError(t, err)

		lease, ok := e.(*leader.LeaseElector)
		require.True(t, ok)
		require.Equal(t, "neutree-core", lease.LeaseName)
		require.Equal(t, "neutree-ns", lease.Namespace)
		require.Equal(t, "neutree-core-abc", lease.Identity)
		require.Equal(t, 15*time.Second, lease.LeaseDuration)
		require.Equal(t, 10*time.Second, lease.RenewDeadline)
		require.Equal(t, 2*time.Second, lease.RetryPeriod)
	})
}

func TestLeaderElectionValidate(t *testing.T) {
	require.NoError(t, NewLeaderElectionOptions().Validate())

	o := NewLeaderElectionOptions()
	o.RenewDeadline = o.LeaseDuration
	require.Error(t, o.Validate())

	o = NewLeaderElectionOptions()
	o.RetryPeriod = o.RenewDeadline
	require.Error(t, o.Validate())

	o = NewLeaderElectionOptions()
	o.Enabled = false
	o.RetryPeriod = 0
	require.NoError(t, o.Validate(), "durations are irrelevant when disabled")
}
