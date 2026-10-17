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
