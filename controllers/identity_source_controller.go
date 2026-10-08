package controllers

import (
	"context"
	"crypto/sha256"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/identitysource"
	"github.com/neutree-ai/neutree/pkg/storage"
)

const (
	// DefaultIdentitySourceRetestInterval is how often an unchanged identity
	// source is tested again.
	DefaultIdentitySourceRetestInterval = 5 * time.Minute
	// identitySourceTestTimeout bounds one connection test.
	identitySourceTestTimeout = 30 * time.Second
	// identitySourceConnectedMessage is the message of a passed test.
	identitySourceConnectedMessage = "connection test passed"
)

// IdentitySourceTester tests the connection of an identity source;
// identitysource.ConnectionTest is the real one.
type IdentitySourceTester func(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) error

// IdentitySourceController keeps the status of identity sources: it tests
// each source's connection when its spec or secrets change and every
// retestInterval otherwise, and removes sources that are being deleted. It
// does not take part in logins; neutree-api reads the sources itself.
type IdentitySourceController struct {
	storage        storage.Storage
	test           IdentitySourceTester
	retestInterval time.Duration
	now            func() time.Time

	// tested holds, per source ID, the fingerprint of the spec and secrets the
	// last recorded test ran with. It is memory only (it hashes the secrets),
	// so a restart tests every source once more.
	mu     sync.Mutex
	tested map[int][sha256.Size]byte
}

type IdentitySourceControllerOption struct {
	Storage storage.Storage
	// Tester defaults to identitysource.ConnectionTest.
	Tester IdentitySourceTester
	// RetestInterval defaults to DefaultIdentitySourceRetestInterval.
	RetestInterval time.Duration
}

func NewIdentitySourceController(option *IdentitySourceControllerOption) (*IdentitySourceController, error) {
	c := &IdentitySourceController{
		storage:        option.Storage,
		test:           option.Tester,
		retestInterval: option.RetestInterval,
		now:            time.Now,
		tested:         map[int][sha256.Size]byte{},
	}

	if c.test == nil {
		c.test = identitysource.ConnectionTest
	}

	if c.retestInterval <= 0 {
		c.retestInterval = DefaultIdentitySourceRetestInterval
	}

	return c, nil
}

func (c *IdentitySourceController) Reconcile(obj interface{}) error {
	source, ok := obj.(*v1.IdentitySource)
	if !ok {
		return errors.New("failed to assert obj to *v1.IdentitySource")
	}

	klog.V(4).Info("Reconcile identity source " + source.GetName())

	return c.sync(source)
}

func (c *IdentitySourceController) sync(obj *v1.IdentitySource) error {
	if obj.Metadata == nil {
		return errors.New("identity source has no metadata")
	}

	id := strconv.Itoa(obj.ID)

	if obj.Metadata.DeletionTimestamp != "" {
		return c.syncDeletion(obj, id)
	}

	if obj.Spec == nil {
		return errors.Errorf("identity source %s has no spec", obj.Metadata.Name)
	}

	secrets, err := c.storage.GetIdentitySourceSecrets(obj.Metadata.Name)
	if err != nil {
		if errors.Is(err, storage.ErrResourceNotFound) {
			// Deleted since it was read; the next pass sees the deletion.
			return nil
		}

		return errors.Wrapf(err, "failed to read secrets of identity source %s", obj.Metadata.Name)
	}

	fingerprint := identitysource.Fingerprint(obj.Spec, secrets)
	if !c.testDue(obj, fingerprint) {
		return nil
	}

	ctx, cancel := context.WithTimeout(context.Background(), identitySourceTestTimeout)
	testErr := c.test(ctx, obj, secrets)

	cancel()

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: c.testedStatus(obj, testErr, secrets)}); err != nil {
		return errors.Wrapf(err, "failed to update status of identity source %s", obj.Metadata.Name)
	}

	if testErr != nil {
		klog.Warningf("Identity source %s failed its connection test: %s", obj.Metadata.Name, identitysource.SafeMessage(testErr, secrets))
	} else {
		klog.V(4).Infof("Identity source %s passed its connection test", obj.Metadata.Name)
	}

	c.mu.Lock()
	c.tested[obj.ID] = fingerprint
	c.mu.Unlock()

	return nil
}

// testDue reports whether the source needs a connection test: it has never
// been tested by this process with its current spec and secrets, or its last
// recorded test is older than the retest interval.
func (c *IdentitySourceController) testDue(obj *v1.IdentitySource, fingerprint [sha256.Size]byte) bool {
	c.mu.Lock()
	last, seen := c.tested[obj.ID]
	c.mu.Unlock()

	if !seen || last != fingerprint {
		return true
	}

	if obj.Status == nil || obj.Status.LastConnectionTest == nil {
		return true
	}

	testedAt, err := time.Parse(time.RFC3339Nano, obj.Status.LastConnectionTest.Time)
	if err != nil {
		return true
	}

	return c.now().Sub(testedAt) >= c.retestInterval
}

func (c *IdentitySourceController) testedStatus(obj *v1.IdentitySource, testErr error, secrets *storage.IdentitySourceSecrets) *v1.IdentitySourceStatus {
	now := c.now().UTC().Format(time.RFC3339Nano)
	status := &v1.IdentitySourceStatus{
		Phase:              v1.IdentitySourcePhaseCONNECTED,
		LastTransitionTime: now,
		LastConnectionTest: &v1.IdentitySourceConnectionTest{Time: now, OK: true, Message: identitySourceConnectedMessage},
	}

	if testErr != nil {
		message := identitysource.SafeMessage(testErr, secrets)
		status.Phase = v1.IdentitySourcePhaseFAILED
		status.ErrorMessage = message
		status.LastConnectionTest.OK = false
		status.LastConnectionTest.Message = message
	}

	// The transition time moves only when the phase does.
	if obj.Status != nil && obj.Status.Phase == status.Phase && obj.Status.LastTransitionTime != "" {
		status.LastTransitionTime = obj.Status.LastTransitionTime
	}

	return status
}

// syncDeletion finishes a soft delete. An identity source owns nothing outside
// its row (its secrets go with it by trigger), so it is marked Deleted and
// removed on the next pass, like other resources.
func (c *IdentitySourceController) syncDeletion(obj *v1.IdentitySource, id string) error {
	if obj.Status != nil && obj.Status.Phase == v1.IdentitySourcePhaseDELETED {
		klog.Infof("Identity source %s already marked as deleted, removing from DB", obj.Metadata.Name)

		if err := c.storage.DeleteIdentitySource(id); err != nil {
			return errors.Wrapf(err, "failed to delete identity source %s from DB", obj.Metadata.Name)
		}

		c.mu.Lock()
		delete(c.tested, obj.ID)
		c.mu.Unlock()

		return nil
	}

	klog.Infof("Deleting identity source %s", obj.Metadata.Name)

	status := &v1.IdentitySourceStatus{
		Phase:              v1.IdentitySourcePhaseDELETED,
		LastTransitionTime: FormatStatusTime(),
	}

	if obj.Status != nil {
		status.LastConnectionTest = obj.Status.LastConnectionTest
	}

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: status}); err != nil {
		return errors.Wrapf(err, "failed to update status of identity source %s", obj.Metadata.Name)
	}

	return nil
}
