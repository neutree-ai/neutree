package controllers

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/identitysource"
	"github.com/neutree-ai/neutree/internal/identitysync"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const testBindPassword = "svc-secret"

type fakeTester struct {
	err   error
	calls int
}

func (f *fakeTester) test(_ context.Context, _ *v1.IdentitySource, _ *storage.IdentitySourceSecrets) error {
	f.calls++
	return f.err
}

func newTestIdentitySourceController(t *testing.T, store *storagemocks.MockStorage, tester *fakeTester, now time.Time) *IdentitySourceController {
	t.Helper()

	c, err := NewIdentitySourceController(&IdentitySourceControllerOption{Storage: store, Tester: tester.test})
	require.NoError(t, err)

	c.now = func() time.Time { return now }

	return c
}

func testIdentitySource() *v1.IdentitySource {
	return &v1.IdentitySource{
		ID:       3,
		Metadata: &v1.Metadata{Name: "corp-ldap"},
		Spec: &v1.IdentitySourceSpec{
			Type:    v1.IdentitySourceTypeLDAP,
			Enabled: true,
			LDAP: &v1.IdentitySourceLDAPSpec{
				URL:        "ldaps://ldap.example.org:636",
				BindDN:     "cn=svc,dc=example,dc=org",
				UserBaseDN: "ou=people,dc=example,dc=org",
				UserFilter: "(uid={username})",
			},
		},
	}
}

func expectSecrets(store *storagemocks.MockStorage, password string) {
	store.EXPECT().GetIdentitySourceSecrets("corp-ldap").
		Return(&storage.IdentitySourceSecrets{LDAPBindPassword: password}, nil).Maybe()
}

// captureStatus records the status written to the source.
func captureStatus(store *storagemocks.MockStorage) *v1.IdentitySourceStatus {
	written := &v1.IdentitySourceStatus{}

	store.EXPECT().UpdateIdentitySource("3", mock.MatchedBy(func(obj *v1.IdentitySource) bool {
		return obj.Spec == nil && obj.Metadata == nil && obj.Status != nil
	})).Run(func(_ string, obj *v1.IdentitySource) { *written = *obj.Status }).Return(nil).Once()

	return written
}

func TestIdentitySourceController_ConnectedSource(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store := storagemocks.NewMockStorage(t)
	tester := &fakeTester{}
	expectSecrets(store, testBindPassword)
	written := captureStatus(store)

	c := newTestIdentitySourceController(t, store, tester, now)

	require.NoError(t, c.Reconcile(testIdentitySource()))

	assert.Equal(t, 1, tester.calls)
	assert.Equal(t, v1.IdentitySourcePhaseCONNECTED, written.Phase)
	assert.Empty(t, written.ErrorMessage)
	require.NotNil(t, written.LastConnectionTest)
	assert.True(t, written.LastConnectionTest.OK)
	assert.Equal(t, now.Format(time.RFC3339Nano), written.LastConnectionTest.Time)
}

func TestIdentitySourceController_FailedSourceKeepsSecretsOut(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store := storagemocks.NewMockStorage(t)
	// A test error that, against every expectation, quotes the password.
	tester := &fakeTester{err: errors.New("ldap: service account bind failed: rejected " + testBindPassword)}
	expectSecrets(store, testBindPassword)
	written := captureStatus(store)

	c := newTestIdentitySourceController(t, store, tester, now)

	require.NoError(t, c.Reconcile(testIdentitySource()))

	assert.Equal(t, v1.IdentitySourcePhaseFAILED, written.Phase)
	require.NotNil(t, written.LastConnectionTest)
	assert.False(t, written.LastConnectionTest.OK)
	assert.Contains(t, written.ErrorMessage, "service account bind failed")
	assert.NotContains(t, written.ErrorMessage, testBindPassword)
	assert.NotContains(t, written.LastConnectionTest.Message, testBindPassword)
}

func TestIdentitySourceController_Cadence(t *testing.T) {
	start := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store := storagemocks.NewMockStorage(t)
	tester := &fakeTester{}
	expectSecrets(store, testBindPassword)

	c := newTestIdentitySourceController(t, store, tester, start)

	// First pass tests and records.
	written := captureStatus(store)
	source := testIdentitySource()
	require.NoError(t, c.Reconcile(source))
	require.Equal(t, 1, tester.calls)

	source.Status = written

	// Unchanged and recently tested: nothing to do.
	c.now = func() time.Time { return start.Add(DefaultIdentitySourceRetestInterval - time.Second) }
	require.NoError(t, c.Reconcile(source))
	assert.Equal(t, 1, tester.calls)

	// The retest interval passed.
	c.now = func() time.Time { return start.Add(DefaultIdentitySourceRetestInterval) }
	written = captureStatus(store)
	require.NoError(t, c.Reconcile(source))
	assert.Equal(t, 2, tester.calls)
	assert.Equal(t, source.Status.LastTransitionTime, written.LastTransitionTime, "the phase did not change")
}

func TestIdentitySourceController_RetestsOnChange(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)

	cases := map[string]func(store *storagemocks.MockStorage, source *v1.IdentitySource){
		"spec": func(store *storagemocks.MockStorage, source *v1.IdentitySource) {
			expectSecrets(store, testBindPassword)
			source.Spec.LDAP.URL = "ldaps://ldap2.example.org:636"
		},
		"secret": func(store *storagemocks.MockStorage, _ *v1.IdentitySource) {
			store.EXPECT().GetIdentitySourceSecrets("corp-ldap").
				Return(&storage.IdentitySourceSecrets{LDAPBindPassword: "rotated"}, nil).Once()
		},
	}

	for name, change := range cases {
		t.Run(name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			tester := &fakeTester{}

			store.EXPECT().GetIdentitySourceSecrets("corp-ldap").
				Return(&storage.IdentitySourceSecrets{LDAPBindPassword: testBindPassword}, nil).Once()

			c := newTestIdentitySourceController(t, store, tester, now)

			written := captureStatus(store)
			source := testIdentitySource()
			require.NoError(t, c.Reconcile(source))

			source.Status = written
			change(store, source)
			captureStatus(store)

			require.NoError(t, c.Reconcile(source))
			assert.Equal(t, 2, tester.calls, "a change is tested at once")
		})
	}
}

func TestIdentitySourceController_UnrecordedTestIsRetried(t *testing.T) {
	now := time.Date(2026, 10, 7, 12, 0, 0, 0, time.UTC)
	store := storagemocks.NewMockStorage(t)
	tester := &fakeTester{}
	expectSecrets(store, testBindPassword)
	store.EXPECT().UpdateIdentitySource("3", mock.Anything).Return(errors.New("postgrest down")).Once()

	c := newTestIdentitySourceController(t, store, tester, now)

	require.Error(t, c.Reconcile(testIdentitySource()))

	captureStatus(store)
	require.NoError(t, c.Reconcile(testIdentitySource()))
	assert.Equal(t, 2, tester.calls)
}

func TestIdentitySourceController_Deletion(t *testing.T) {
	t.Run("marks a soft-deleted source Deleted without testing it", func(t *testing.T) {
		store := storagemocks.NewMockStorage(t)
		tester := &fakeTester{}
		written := captureStatus(store)

		source := testIdentitySource()
		source.Metadata.DeletionTimestamp = "2026-10-07T12:00:00Z"
		source.Status = &v1.IdentitySourceStatus{
			Phase:              v1.IdentitySourcePhaseCONNECTED,
			LastConnectionTest: &v1.IdentitySourceConnectionTest{Time: "2026-10-07T11:59:00Z", OK: true},
		}

		c := newTestIdentitySourceController(t, store, tester, time.Now())

		require.NoError(t, c.Reconcile(source))
		assert.Equal(t, v1.IdentitySourcePhaseDELETED, written.Phase)
		assert.Equal(t, source.Status.LastConnectionTest, written.LastConnectionTest)
		assert.Zero(t, tester.calls)
	})

	t.Run("removes a Deleted source", func(t *testing.T) {
		store := storagemocks.NewMockStorage(t)
		tester := &fakeTester{}
		store.EXPECT().DeleteIdentitySource("3").Return(nil).Once()

		source := testIdentitySource()
		source.Metadata.DeletionTimestamp = "2026-10-07T12:00:00Z"
		source.Status = &v1.IdentitySourceStatus{Phase: v1.IdentitySourcePhaseDELETED}

		c := newTestIdentitySourceController(t, store, tester, time.Now())
		c.tested[source.ID] = [32]byte{1}

		require.NoError(t, c.Reconcile(source))
		assert.NotContains(t, c.tested, source.ID)
		assert.Zero(t, tester.calls)
	})

	t.Run("delete error is returned", func(t *testing.T) {
		store := storagemocks.NewMockStorage(t)
		store.EXPECT().DeleteIdentitySource("3").Return(errors.New("postgrest down")).Once()

		source := testIdentitySource()
		source.Metadata.DeletionTimestamp = "2026-10-07T12:00:00Z"
		source.Status = &v1.IdentitySourceStatus{Phase: v1.IdentitySourcePhaseDELETED}

		c := newTestIdentitySourceController(t, store, &fakeTester{}, time.Now())

		assert.Error(t, c.Reconcile(source))
	})
}

// Deleted between the list and the secrets read: nothing is written.
func TestIdentitySourceController_GoneMeanwhile(t *testing.T) {
	store := storagemocks.NewMockStorage(t)
	tester := &fakeTester{}
	store.EXPECT().GetIdentitySourceSecrets("corp-ldap").Return(nil, storage.ErrResourceNotFound).Once()

	c := newTestIdentitySourceController(t, store, tester, time.Now())

	require.NoError(t, c.Reconcile(testIdentitySource()))
	assert.Zero(t, tester.calls)
}

func TestIdentitySourceController_WrongObject(t *testing.T) {
	c := newTestIdentitySourceController(t, storagemocks.NewMockStorage(t), &fakeTester{}, time.Now())

	assert.Error(t, c.Reconcile(&v1.Role{}))
}

type fakeSyncer struct {
	result identitysync.Result
	err    error
	calls  int
}

func (f *fakeSyncer) sync(_ context.Context, _ *v1.IdentitySource, _ *storage.IdentitySourceSecrets) (identitysync.Result, error) {
	f.calls++
	return f.result, f.err
}

func newSyncingController(t *testing.T, store *storagemocks.MockStorage, syncer *fakeSyncer, now *time.Time) *IdentitySourceController {
	t.Helper()

	c, err := NewIdentitySourceController(&IdentitySourceControllerOption{
		Storage: store,
		Tester:  (&fakeTester{}).test,
		Syncer:  syncer.sync,
	})
	require.NoError(t, err)

	c.now = func() time.Time { return *now }

	return c
}

// syncedSource is a tested LDAP source with sync enabled whose last sync
// handled the request at handled and finished at finished.
func syncedSource(now time.Time, finished time.Time, handled, requested string, ok bool) *v1.IdentitySource {
	src := testIdentitySource()
	src.Spec.Sync = &v1.IdentitySourceSyncSpec{Enabled: true, Interval: 3600, RequestedAt: requested}
	src.Status = &v1.IdentitySourceStatus{
		Phase:              v1.IdentitySourcePhaseCONNECTED,
		LastConnectionTest: &v1.IdentitySourceConnectionTest{Time: now.Format(time.RFC3339Nano), OK: true},
	}

	if !finished.IsZero() {
		src.Status.LastSync = &v1.IdentitySourceSyncStatus{
			RequestedAt: handled,
			StartedAt:   finished.Add(-time.Second).Format(time.RFC3339Nano),
			FinishedAt:  finished.Format(time.RFC3339Nano),
			OK:          ok,
		}
	}

	return src
}

// primeTested makes the controller consider src tested with its current
// spec, so only the sync decision is exercised.
func primeTested(c *IdentitySourceController, src *v1.IdentitySource) {
	secrets := &storage.IdentitySourceSecrets{LDAPBindPassword: testBindPassword}
	c.tested[src.ID] = identitysource.Fingerprint(src.Spec, secrets)
}

func TestIdentitySourceController_FirstSyncAndStatus(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	store := storagemocks.NewMockStorage(t)
	syncer := &fakeSyncer{result: identitysync.Result{Planned: 12, Created: 7, Memberships: 5}}
	c := newSyncingController(t, store, syncer, &now)

	src := syncedSource(now, time.Time{}, "", "", true)
	primeTested(c, src)
	expectSecrets(store, testBindPassword)
	written := captureStatus(store)

	require.NoError(t, c.Reconcile(src))

	assert.Equal(t, 1, syncer.calls)
	require.NotNil(t, written.LastSync)
	assert.True(t, written.LastSync.OK)
	assert.Equal(t, 7, written.LastSync.Created)
	assert.Equal(t, 5, written.LastSync.Memberships)
	assert.Zero(t, written.LastSync.Pending)
	assert.Equal(t, now.Format(time.RFC3339Nano), written.LastSync.LastSuccessTime)
	assert.Equal(t, "applied 12 changes", written.LastSync.Message)
	// The connection test part of the status is kept.
	assert.Equal(t, v1.IdentitySourcePhaseCONNECTED, written.Phase)
	assert.NotNil(t, written.LastConnectionTest)
}

func TestIdentitySourceController_SyncCadence(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	handled := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)

	cases := []struct {
		name     string
		src      *v1.IdentitySource
		mutate   func(*v1.IdentitySource)
		wantSync bool
	}{
		{"recent sync waits", syncedSource(now, now.Add(-10*time.Minute), handled, handled, true), nil, false},
		{"interval passed", syncedSource(now, now.Add(-61*time.Minute), handled, handled, true), nil, true},
		{"new manual request", syncedSource(now, now.Add(-time.Minute), handled, now.Add(-30*time.Second).Format(time.RFC3339Nano), true), nil, true},
		{"request handled already", syncedSource(now, now.Add(-time.Minute), handled, handled, true), nil, false},
		{"failed sync retries sooner", syncedSource(now, now.Add(-6*time.Minute), handled, handled, false), nil, true},
		{"failed sync waits the retest interval", syncedSource(now, now.Add(-time.Minute), handled, handled, false), nil, false},
		{"sync disabled", syncedSource(now, time.Time{}, "", "", true), func(s *v1.IdentitySource) { s.Spec.Sync.Enabled = false }, false},
		{"no sync spec", syncedSource(now, time.Time{}, "", "", true), func(s *v1.IdentitySource) { s.Spec.Sync = nil }, false},
		{"oidc source is not synced", syncedSource(now, time.Time{}, "", "", true), func(s *v1.IdentitySource) {
			s.Spec.Type = v1.IdentitySourceTypeOIDC
			s.Spec.LDAP = nil
			s.Spec.OIDC = &v1.IdentitySourceOIDCSpec{Issuer: "https://idp.example.org"}
		}, false},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			store := storagemocks.NewMockStorage(t)
			syncer := &fakeSyncer{}
			c := newSyncingController(t, store, syncer, &now)

			if tc.mutate != nil {
				tc.mutate(tc.src)
			}

			primeTested(c, tc.src)
			expectSecrets(store, testBindPassword)

			if tc.wantSync {
				captureStatus(store)
			}

			require.NoError(t, c.Reconcile(tc.src))
			assert.Equal(t, tc.wantSync, syncer.calls == 1)
		})
	}
}

// A spec change seen by the process syncs at once; a fresh process does not
// sync merely because it has not seen the spec before.
func TestIdentitySourceController_SyncsOnSpecChange(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	handled := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	store := storagemocks.NewMockStorage(t)
	syncer := &fakeSyncer{}
	c := newSyncingController(t, store, syncer, &now)
	expectSecrets(store, testBindPassword)

	src := syncedSource(now, now.Add(-time.Minute), handled, handled, true)
	primeTested(c, src)

	require.NoError(t, c.Reconcile(src))
	assert.Zero(t, syncer.calls)

	src.Spec.LDAP.Sync = &v1.IdentitySourceLDAPSyncSpec{OrgUnitBaseDN: "ou=org,dc=example,dc=org"}
	primeTested(c, src)
	captureStatus(store)

	require.NoError(t, c.Reconcile(src))
	assert.Equal(t, 1, syncer.calls)
}

func TestIdentitySourceController_FailedSyncStatus(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	earlier := now.Add(-2 * time.Hour)
	store := storagemocks.NewMockStorage(t)
	syncer := &fakeSyncer{
		result: identitysync.Result{Planned: 10, Created: 3},
		err:    errors.New("create team g1: bind " + testBindPassword + " refused"),
	}
	c := newSyncingController(t, store, syncer, &now)

	requested := now.Add(-time.Second).Format(time.RFC3339Nano)
	src := syncedSource(now, earlier, "", requested, true)
	src.Status.LastSync.LastSuccessTime = earlier.Format(time.RFC3339Nano)
	primeTested(c, src)
	expectSecrets(store, testBindPassword)
	written := captureStatus(store)

	require.NoError(t, c.Reconcile(src))

	require.NotNil(t, written.LastSync)
	assert.False(t, written.LastSync.OK)
	assert.Equal(t, 3, written.LastSync.Created)
	assert.Equal(t, 7, written.LastSync.Pending)
	assert.Equal(t, requested, written.LastSync.RequestedAt)
	assert.Equal(t, earlier.Format(time.RFC3339Nano), written.LastSync.LastSuccessTime)
	assert.NotContains(t, written.LastSync.Message, testBindPassword)
	assert.Contains(t, written.LastSync.Message, "create team g1")
}

// The connection test writes the whole status; it keeps the sync result.
func TestIdentitySourceController_TestKeepsSyncStatus(t *testing.T) {
	now := time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC)
	handled := now.Add(-2 * time.Hour).Format(time.RFC3339Nano)
	store := storagemocks.NewMockStorage(t)
	syncer := &fakeSyncer{}
	c := newSyncingController(t, store, syncer, &now)

	src := syncedSource(now, now.Add(-time.Minute), handled, handled, true)
	expectSecrets(store, testBindPassword)
	written := captureStatus(store)

	require.NoError(t, c.Reconcile(src))

	assert.Zero(t, syncer.calls)
	require.NotNil(t, written.LastSync)
	assert.Equal(t, src.Status.LastSync.FinishedAt, written.LastSync.FinishedAt)
}

func TestRequestedAfter(t *testing.T) {
	assert.False(t, requestedAfter("", ""))
	assert.True(t, requestedAfter("2026-10-09T12:00:00+00:00", ""))
	assert.True(t, requestedAfter("2026-10-09T12:00:01+00:00", "2026-10-09T12:00:00Z"))
	assert.False(t, requestedAfter("2026-10-09T12:00:00+00:00", "2026-10-09T12:00:00Z"))
	assert.False(t, requestedAfter("2026-10-09T11:00:00+00:00", "2026-10-09T12:00:00Z"))
}
