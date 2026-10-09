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
	"github.com/neutree-ai/neutree/pkg/identity/ldap"
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
// LDAPSyncer is the real one.
type IdentitySourceSyncer func(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) (identitysync.Result, error)

// LDAPSyncer reads the directory of an LDAP source and syncs it with syncer.
func LDAPSyncer(syncer *identitysync.Syncer) IdentitySourceSyncer {
	return func(ctx context.Context, source *v1.IdentitySource, secrets *storage.IdentitySourceSecrets) (identitysync.Result, error) {
		cfg := identitysource.LDAPConfig(source.Spec.LDAP, secrets.LDAPBindPassword)

		dir, err := ldap.NewDirectory(cfg, ldap.NewDialer(cfg))
		if err != nil {
			return identitysync.Result{}, fmt.Errorf("%w: %w", identitysync.ErrReadDirectory, err)
		}

		return syncer.Run(ctx, source.Metadata.Name, dir)
	}
}

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

	// tested holds, per source ID, the fingerprint of the spec and secrets the
	// last recorded test ran with; synced the same for the last sync (without
	// spec.sync.requested_at). They are memory only (they hash the secrets),
	// so a restart tests every source once more.
	mu     sync.Mutex
	tested map[int][sha256.Size]byte
	synced map[int][sha256.Size]byte
}

type IdentitySourceControllerOption struct {
	Storage storage.Storage
	// Tester defaults to identitysource.ConnectionTest.
	Tester IdentitySourceTester
	// Syncer defaults to LDAPSyncer writing to Storage and AuthClient. With
	// neither Syncer nor AuthClient, sources are not synced.
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
		tested:         map[int][sha256.Size]byte{},
		synced:         map[int][sha256.Size]byte{},
	}

	if c.test == nil {
		c.test = identitysource.ConnectionTest
	}

	if c.syncer == nil && option.AuthClient != nil {
		c.syncer = LDAPSyncer(&identitysync.Syncer{Store: option.Storage, Auth: option.AuthClient})
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
	if c.testDue(obj, fingerprint) {
		status = c.runTest(obj, id, secrets)
		if status == nil {
			return errors.Errorf("failed to update status of identity source %s", obj.Metadata.Name)
		}

		c.mu.Lock()
		c.tested[obj.ID] = fingerprint
		c.mu.Unlock()
	}

	syncFingerprint := identitysource.Fingerprint(syncSpec(obj.Spec), secrets)
	if !c.syncDue(obj, status, syncFingerprint) {
		return nil
	}

	next := &v1.IdentitySourceStatus{}

	if status != nil {
		copied := *status
		next = &copied
	}

	next.LastSync = c.runSync(obj, secrets, status)

	c.mu.Lock()
	c.synced[obj.ID] = syncFingerprint
	c.mu.Unlock()

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: next}); err != nil {
		return errors.Wrapf(err, "failed to update sync status of identity source %s", obj.Metadata.Name)
	}

	return nil
}

// runTest tests the connection and writes the status. It returns the status
// written, or nil when it could not be written.
func (c *IdentitySourceController) runTest(obj *v1.IdentitySource, id string, secrets *storage.IdentitySourceSecrets) *v1.IdentitySourceStatus {
	ctx, cancel := context.WithTimeout(context.Background(), identitySourceTestTimeout)
	testErr := c.test(ctx, obj, secrets)

	cancel()

	status := c.testedStatus(obj, testErr, secrets)
	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: status}); err != nil {
		klog.Errorf("Failed to update status of identity source %s: %v", obj.Metadata.Name, err)
		return nil
	}

	if testErr != nil {
		klog.Warningf("Identity source %s failed its connection test: %s", obj.Metadata.Name, identitysource.SafeMessage(testErr, secrets))
	} else {
		klog.V(4).Infof("Identity source %s passed its connection test", obj.Metadata.Name)
	}

	return status
}

// syncSpec is the part of spec a sync depends on: all of it but the sync
// request, which triggers a sync by itself.
func syncSpec(spec *v1.IdentitySourceSpec) *v1.IdentitySourceSpec {
	copied := *spec

	if spec.Sync != nil {
		syncCopy := *spec.Sync
		syncCopy.RequestedAt = ""
		copied.Sync = &syncCopy
	}

	return &copied
}

// syncDue reports whether the source needs an organization sync now; see
// IdentitySourceController.
func (c *IdentitySourceController) syncDue(obj *v1.IdentitySource, status *v1.IdentitySourceStatus, fingerprint [sha256.Size]byte) bool {
	if c.syncer == nil || !obj.Spec.SyncEnabled() {
		return false
	}

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

	c.mu.Lock()

	previous, seen := c.synced[obj.ID]
	if !seen {
		// A fresh process has nothing to compare with; it remembers what it
		// sees and waits for the interval.
		c.synced[obj.ID] = fingerprint
	}
	c.mu.Unlock()

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

	// The status is written whole; keep what the sync reported.
	if obj.Status != nil {
		status.LastSync = obj.Status.LastSync
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
		delete(c.synced, obj.ID)
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
		status.LastSync = obj.Status.LastSync
	}

	if err := c.storage.UpdateIdentitySource(id, &v1.IdentitySource{Status: status}); err != nil {
		return errors.Wrapf(err, "failed to update status of identity source %s", obj.Metadata.Name)
	}

	return nil
}
