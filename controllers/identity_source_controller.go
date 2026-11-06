package controllers

import (
	"context"
	"crypto/sha256"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/auth"
	"github.com/neutree-ai/neutree/internal/identitysource"
	"github.com/neutree-ai/neutree/internal/identitysync"
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
	// identitySourceSyncTimeout bounds one organization sync.
	identitySourceSyncTimeout = 30 * time.Minute
)

// IdentitySourceTester tests the connection of an identity source;
// identitysource.ConnectionTest is the real one.
type IdentitySourceTester func(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) error

// IdentitySourceSyncer runs one organization sync of an LDAP identity source;
// (*identitysync.Syncer).RunSource is the real one.
type IdentitySourceSyncer func(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) (identitysync.Result, error)

// IdentitySourceController keeps the status of identity sources: it tests
// each source's connection when its spec or secrets change and every
// retestInterval otherwise, runs the organization sync of LDAP sources that
// enable it, and removes sources that are being deleted. It does not take
// part in logins; neutree-api reads the sources itself.
//
// A sync runs when the source has never synced, when spec.sync.requested_at
// is later than the request the last sync handled, when this process sees
// the spec or secrets change, and every spec.sync.interval otherwise (after a
// failed sync, at most every retestInterval). The workqueue never hands the
// same source to two workers at once, so syncs of one source do not overlap.
type IdentitySourceController struct {
	storage        storage.Storage
	test           IdentitySourceTester
	syncer         IdentitySourceSyncer
	retestInterval time.Duration
	now            func() time.Time

	// tested holds, per source ID, the last recorded test; synced the
	// fingerprint of the spec (without spec.sync.requested_at) and secrets the
	// last sync ran with. They are memory only (they hash the secrets), so a
	// restart tests every source once more.
	mu     sync.Mutex
	tested map[int]identitySourceTest
	synced map[int][sha256.Size]byte
}

type identitySourceTest struct {
	// fingerprint is of the spec and secrets the test ran with.
	fingerprint [sha256.Size]byte
	at          time.Time
}

type IdentitySourceControllerOption struct {
	Storage storage.Storage
	// Tester defaults to identitysource.ConnectionTest.
	Tester IdentitySourceTester
	// Syncer defaults to an identitysync.Syncer writing to Storage and
	// AuthClient. With neither Syncer nor AuthClient, sources are not synced.
	Syncer     IdentitySourceSyncer
	AuthClient auth.Client
	// RetestInterval defaults to DefaultIdentitySourceRetestInterval.
	RetestInterval time.Duration
}

func NewIdentitySourceController(option *IdentitySourceControllerOption) (*IdentitySourceController, error) {
	c := &IdentitySourceController{
		storage:        option.Storage,
		test:           option.Tester,
		retestInterval: option.RetestInterval,
		syncer:         option.Syncer,
		now:            time.Now,
		tested:         map[int]identitySourceTest{},
		synced:         map[int][sha256.Size]byte{},
	}

	if c.test == nil {
		c.test = identitysource.ConnectionTest
	}

	if c.syncer == nil && option.AuthClient != nil {
		c.syncer = (&identitysync.Syncer{Store: option.Storage, Auth: option.AuthClient}).RunSource
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

	return c.reconcile(source)
}

func (c *IdentitySourceController) reconcile(obj *v1.IdentitySource) error {
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

	status := obj.Status

	fingerprint := identitysource.Fingerprint(obj.Spec, secrets)
	if c.testDue(obj.ID, fingerprint) {
		status, err = c.runTest(obj, id, secrets, fingerprint)
		if err != nil {
			return err
		}
	}

	if c.syncer == nil || !obj.Spec.SyncEnabled() {
		return nil
	}

	syncPrint := identitysource.SyncFingerprint(obj.Spec, secrets)

	c.mu.Lock()
	previous, seen := c.synced[obj.ID]
	c.mu.Unlock()

	if !c.syncDue(obj, status, syncPrint, previous, seen) {
		if !seen {
			// A fresh process has nothing to compare with; it remembers what
			// it sees and waits for the interval.
			c.rememberSync(obj.ID, syncPrint)
		}

		return nil
	}

	next := copyStatus(status)
	next.LastSync = c.runSync(obj, secrets, status)

	c.rememberSync(obj.ID, syncPrint)

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: next}); err != nil {
		return errors.Wrapf(err, "failed to update sync status of identity source %s", obj.Metadata.Name)
	}

	return nil
}

// runTest tests the connection, writes the status and records the test. It
// returns the status written.
func (c *IdentitySourceController) runTest(
	obj *v1.IdentitySource, id string, secrets *storage.IdentitySourceSecrets, fingerprint [sha256.Size]byte,
) (*v1.IdentitySourceStatus, error) {
	ctx, cancel := context.WithTimeout(context.Background(), identitySourceTestTimeout)
	testErr := c.test(ctx, obj, secrets)

	cancel()

	testedAt := c.now()

	status := c.testedStatus(obj, testedAt, testErr, secrets)
	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: status}); err != nil {
		return nil, errors.Wrapf(err, "failed to update status of identity source %s", obj.Metadata.Name)
	}

	if testErr != nil {
		klog.Warningf("Identity source %s failed its connection test: %s", obj.Metadata.Name, identitysource.SafeMessage(testErr, secrets))
	} else {
		klog.V(4).Infof("Identity source %s passed its connection test", obj.Metadata.Name)
	}

	c.mu.Lock()
	c.tested[obj.ID] = identitySourceTest{fingerprint: fingerprint, at: testedAt}
	c.mu.Unlock()

	return status, nil
}

func (c *IdentitySourceController) rememberSync(id int, fingerprint [sha256.Size]byte) {
	c.mu.Lock()
	c.synced[id] = fingerprint
	c.mu.Unlock()
}

// syncDue reports whether a source that enables the sync needs one now;
// previous is the fingerprint of the last sync this process saw, if seen. See
// IdentitySourceController.
func (c *IdentitySourceController) syncDue(
	obj *v1.IdentitySource, status *v1.IdentitySourceStatus, fingerprint, previous [sha256.Size]byte, seen bool,
) bool {
	var last *v1.IdentitySourceSyncStatus
	if status != nil {
		last = status.LastSync
	}

	if last == nil || last.StartedAt == "" {
		return true
	}

	if requestedAfter(obj.Spec.Sync.RequestedAt, last.RequestedAt) {
		return true
	}

	if seen && previous != fingerprint {
		return true
	}

	interval := obj.Spec.Sync.SyncInterval()
	if !last.OK && c.retestInterval < interval {
		interval = c.retestInterval
	}

	finished := last.FinishedAt
	if finished == "" {
		finished = last.StartedAt
	}

	at, err := time.Parse(time.RFC3339Nano, finished)
	if err != nil {
		return true
	}

	return c.now().Sub(at) >= interval
}

// requestedAfter reports whether the sync request requested is later than
// the request handled; an unparsable request counts as new once.
func requestedAfter(requested, handled string) bool {
	if requested == "" {
		return false
	}

	if handled == "" {
		return true
	}

	r, err := time.Parse(time.RFC3339Nano, requested)
	if err != nil {
		return requested != handled
	}

	h, err := time.Parse(time.RFC3339Nano, handled)
	if err != nil {
		return true
	}

	return r.After(h)
}

// runSync runs one organization sync and returns its status. The stored
// OrgUnits, Teams and users are whatever the sync got to write; a failure is
// reported, not rolled back.
func (c *IdentitySourceController) runSync(obj *v1.IdentitySource, secrets *storage.IdentitySourceSecrets, status *v1.IdentitySourceStatus) *v1.IdentitySourceSyncStatus {
	started := c.now().UTC()

	ctx, cancel := context.WithTimeout(context.Background(), identitySourceSyncTimeout)
	result, err := c.syncer(ctx, obj, secrets)

	cancel()

	finished := c.now().UTC()
	last := &v1.IdentitySourceSyncStatus{
		RequestedAt: obj.Spec.Sync.RequestedAt,
		StartedAt:   started.Format(time.RFC3339Nano),
		FinishedAt:  finished.Format(time.RFC3339Nano),
		OK:          err == nil,
		Created:     result.Created,
		Updated:     result.Updated,
		Reactivated: result.Reactivated,
		Deactivated: result.Deactivated,
		Memberships: result.Memberships,
		Pending:     result.Pending(),
	}

	if err != nil {
		last.Message = identitysource.SafeMessage(err, secrets)
		if status != nil && status.LastSync != nil {
			last.LastSuccessTime = status.LastSync.LastSuccessTime
		}

		klog.Warningf("Identity source %s: sync failed after %d of %d writes: %s",
			obj.Metadata.Name, result.Applied(), result.Planned, last.Message)

		return last
	}

	last.LastSuccessTime = last.FinishedAt

	if result.Planned == 0 {
		last.Message = "in sync, no changes"
	} else {
		last.Message = fmt.Sprintf("applied %d changes", result.Applied())
	}

	klog.Infof("Identity source %s: sync applied %d writes (created %d, updated %d, reactivated %d, deactivated %d, memberships %d)",
		obj.Metadata.Name, result.Applied(), result.Created, result.Updated, result.Reactivated, result.Deactivated, result.Memberships)

	return last
}

// testDue reports whether the source needs a connection test: this process
// has not recorded one with its current spec and secrets, or the last one is
// older than the retest interval.
func (c *IdentitySourceController) testDue(id int, fingerprint [sha256.Size]byte) bool {
	c.mu.Lock()
	last, seen := c.tested[id]
	c.mu.Unlock()

	return !seen || last.fingerprint != fingerprint || c.now().Sub(last.at) >= c.retestInterval
}

func (c *IdentitySourceController) testedStatus(
	obj *v1.IdentitySource, testedAt time.Time, testErr error, secrets *storage.IdentitySourceSecrets,
) *v1.IdentitySourceStatus {
	now := testedAt.UTC().Format(time.RFC3339Nano)

	// The status is written whole; start from the stored one so that what
	// the sync reported stays.
	status := copyStatus(obj.Status)
	status.Phase = v1.IdentitySourcePhaseCONNECTED
	status.ErrorMessage = ""
	status.LastTransitionTime = now
	status.LastConnectionTest = &v1.IdentitySourceConnectionTest{Time: now, OK: true, Message: identitySourceConnectedMessage}

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

// copyStatus returns a copy of status to change, or an empty status.
func copyStatus(status *v1.IdentitySourceStatus) *v1.IdentitySourceStatus {
	if status == nil {
		return &v1.IdentitySourceStatus{}
	}

	copied := *status

	return &copied
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
		delete(c.synced, obj.ID)
		c.mu.Unlock()

		return nil
	}

	klog.Infof("Deleting identity source %s", obj.Metadata.Name)

	status := copyStatus(obj.Status)
	status.Phase = v1.IdentitySourcePhaseDELETED
	status.LastTransitionTime = FormatStatusTime()
	status.ErrorMessage = ""

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: status}); err != nil {
		return errors.Wrapf(err, "failed to update status of identity source %s", obj.Metadata.Name)
	}

	return nil
}
