package options

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/pkg/errors"
	"github.com/spf13/pflag"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/leaderelection"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/leader"
)

const serviceAccountNamespaceFile = "/var/run/secrets/kubernetes.io/serviceaccount/namespace"

type LeaderElectionOptions struct {
	Enabled       bool
	LeaseName     string
	Namespace     string
	LeaseDuration time.Duration
	RenewDeadline time.Duration
	RetryPeriod   time.Duration
}

func NewLeaderElectionOptions() *LeaderElectionOptions {
	return &LeaderElectionOptions{
		Enabled:       true,
		LeaseName:     "neutree-core-leader",
		Namespace:     os.Getenv("POD_NAMESPACE"),
		LeaseDuration: 15 * time.Second,
		RenewDeadline: 10 * time.Second,
		RetryPeriod:   2 * time.Second,
	}
}

func (o *LeaderElectionOptions) AddFlags(fs *pflag.FlagSet) {
	fs.BoolVar(&o.Enabled, "leader-elect", o.Enabled,
		"Elect a leader through a Kubernetes Lease so only one replica runs controllers and crons. Ignored outside Kubernetes.")
	fs.StringVar(&o.LeaseName, "leader-elect-lease-name", o.LeaseName, "Name of the leader election Lease.")
	fs.StringVar(&o.Namespace, "leader-elect-namespace", o.Namespace,
		"Namespace of the leader election Lease. Defaults to $POD_NAMESPACE, then the service account namespace.")
	fs.DurationVar(&o.LeaseDuration, "leader-elect-lease-duration", o.LeaseDuration,
		"How long standbys wait before taking over an unrenewed lease.")
	fs.DurationVar(&o.RenewDeadline, "leader-elect-renew-deadline", o.RenewDeadline,
		"How long the leader keeps retrying to renew before giving up leadership.")
	fs.DurationVar(&o.RetryPeriod, "leader-elect-retry-period", o.RetryPeriod,
		"Interval between attempts to acquire or renew the lease.")
}

func (o *LeaderElectionOptions) Validate() error {
	if !o.Enabled {
		return nil
	}

	if o.LeaseName == "" {
		return errors.New("--leader-elect-lease-name must not be empty")
	}

	if o.RetryPeriod <= 0 || o.RenewDeadline <= 0 || o.LeaseDuration <= 0 {
		return errors.New("leader election durations must be positive")
	}

	if o.LeaseDuration <= o.RenewDeadline {
		return errors.New("--leader-elect-lease-duration must be greater than --leader-elect-renew-deadline")
	}

	if float64(o.RenewDeadline) <= leaderelection.JitterFactor*float64(o.RetryPeriod) {
		return fmt.Errorf("--leader-elect-renew-deadline must be greater than %.1f x --leader-elect-retry-period",
			leaderelection.JitterFactor)
	}

	return nil
}

// Elector returns a Lease-based elector when running in Kubernetes, and
// AlwaysLeader otherwise so single-instance deployments keep their behavior.
func (o *LeaderElectionOptions) Elector(inClusterConfig func() (*rest.Config, error)) (leader.Elector, error) {
	if !o.Enabled {
		klog.Info("Leader election disabled; running as leader")
		return leader.AlwaysLeader{}, nil
	}

	restConfig, err := inClusterConfig()
	if errors.Is(err, rest.ErrNotInCluster) {
		klog.Info("Not running in Kubernetes; leader election skipped, running as leader")
		return leader.AlwaysLeader{}, nil
	}

	if err != nil {
		return nil, errors.Wrap(err, "failed to load in-cluster config for leader election")
	}

	namespace, err := o.namespace()
	if err != nil {
		return nil, err
	}

	identity, err := podIdentity()
	if err != nil {
		return nil, err
	}

	client, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, errors.Wrap(err, "failed to create kubeclient for leader election")
	}

	return &leader.LeaseElector{
		Client:        client,
		LeaseName:     o.LeaseName,
		Namespace:     namespace,
		Identity:      identity,
		LeaseDuration: o.LeaseDuration,
		RenewDeadline: o.RenewDeadline,
		RetryPeriod:   o.RetryPeriod,
	}, nil
}

func (o *LeaderElectionOptions) namespace() (string, error) {
	if o.Namespace != "" {
		return o.Namespace, nil
	}

	data, err := os.ReadFile(serviceAccountNamespaceFile)
	if err == nil && strings.TrimSpace(string(data)) != "" {
		return strings.TrimSpace(string(data)), nil
	}

	return "", fmt.Errorf("leader election namespace unknown: set --leader-elect-namespace or POD_NAMESPACE")
}

func podIdentity() (string, error) {
	if name := os.Getenv("POD_NAME"); name != "" {
		return name, nil
	}

	// A pod's hostname is its name unless spec.hostname overrides it.
	name, err := os.Hostname()
	if err != nil {
		return "", errors.Wrap(err, "failed to determine leader election identity")
	}

	return name, nil
}
