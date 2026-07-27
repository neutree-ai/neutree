package image

import (
	"context"
	"encoding/base64"
	"net/http"
	"testing"

	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/client"
)

const testRegistryHost = "registry.example.com"

type fakeLister struct {
	withCreds    []v1.ImageRegistry
	withCredsErr error
	masked       []v1.ImageRegistry
	maskedErr    error

	calls []client.ImageRegistryListOptions
}

func (f *fakeLister) List(opts client.ImageRegistryListOptions) ([]v1.ImageRegistry, error) {
	f.calls = append(f.calls, opts)

	if opts.WithCreds {
		return f.withCreds, f.withCredsErr
	}

	return f.masked, f.maskedErr
}

type pushCall struct {
	source string
	target string
	auth   string
}

type fakePusher struct {
	loaded  []string
	pushed  []pushCall
	loadErr error
	pushErr error
}

func (f *fakePusher) LoadArchive(_ context.Context, archivePath string) error {
	f.loaded = append(f.loaded, archivePath)
	return f.loadErr
}

func (f *fakePusher) TagAndPush(_ context.Context, source, target, registryAuth string) error {
	f.pushed = append(f.pushed, pushCall{source: source, target: target, auth: registryAuth})
	return f.pushErr
}

func testRegistry(url, repository string, auth v1.ImageRegistryAuthConfig) v1.ImageRegistry {
	return v1.ImageRegistry{
		Metadata: &v1.Metadata{Name: "default", Workspace: "default"},
		Spec: &v1.ImageRegistrySpec{
			URL:        url,
			Repository: repository,
			AuthConfig: auth,
		},
	}
}

func credentialedRegistry() v1.ImageRegistry {
	return testRegistry(testRegistryHost, "neutree", v1.ImageRegistryAuthConfig{
		Username: "user",
		Password: "pass",
	})
}

func maskedRegistry() v1.ImageRegistry {
	return testRegistry(testRegistryHost, "neutree", v1.ImageRegistryAuthConfig{})
}

func apiError(status int) error {
	return &client.APIError{Expected: []int{http.StatusOK}, StatusCode: status, Body: "denied"}
}

// expectedAuth is the blob the production code builds for the shared test credentials.
func expectedAuth(t *testing.T) string {
	t.Helper()

	auth, err := util.EncodeRegistryAuth("user", "pass", testRegistryHost)
	require.NoError(t, err)

	return auth
}

func baseOptions() PushOptions {
	return PushOptions{Workspace: "default", Registry: "default", SourceImage: "my-workload:v1"}
}

const digest = "@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

func TestPushTargetReference(t *testing.T) {
	tests := []struct {
		name           string
		url            string
		repository     string
		source         string
		target         string
		expectedSource string
		expectedTarget string
	}{
		{
			name:           "bare image gets registry and project prefix",
			url:            "https://" + testRegistryHost,
			repository:     "neutree",
			source:         "my-workload:v1",
			expectedSource: "my-workload:v1",
			expectedTarget: testRegistryHost + "/neutree/my-workload:v1",
		},
		{
			name:           "untagged image is normalized to latest",
			url:            testRegistryHost,
			repository:     "neutree",
			source:         "my-workload",
			expectedSource: "my-workload:latest",
			expectedTarget: testRegistryHost + "/neutree/my-workload:latest",
		},
		{
			name:           "source registry host is replaced",
			url:            testRegistryHost,
			repository:     "neutree",
			source:         "ghcr.io/acme/my-workload:v1",
			expectedSource: "ghcr.io/acme/my-workload:v1",
			expectedTarget: testRegistryHost + "/neutree/acme/my-workload:v1",
		},
		{
			name:           "registry without project",
			url:            testRegistryHost + ":5000",
			source:         "my-workload:v1",
			expectedSource: "my-workload:v1",
			expectedTarget: testRegistryHost + ":5000/my-workload:v1",
		},
		{
			name:           "target overrides the repository path",
			url:            testRegistryHost,
			repository:     "neutree",
			source:         "my-workload:v1",
			target:         "team-a/renamed:v2",
			expectedSource: "my-workload:v1",
			expectedTarget: testRegistryHost + "/neutree/team-a/renamed:v2",
		},
		{
			name:           "image already in the target registry is pushed as is",
			url:            testRegistryHost,
			repository:     "neutree",
			source:         testRegistryHost + "/neutree/my-workload:v1",
			expectedSource: testRegistryHost + "/neutree/my-workload:v1",
			expectedTarget: testRegistryHost + "/neutree/my-workload:v1",
		},
		{
			// The pull-side rewrite skips Docker Hub prefixes; a push must not,
			// or the image lands on the source registry with another registry's
			// credentials in hand.
			name:           "docker hub registry still receives the prefix",
			url:            "docker.io",
			repository:     "myorg",
			source:         "ghcr.io/acme/tiny:v1",
			expectedSource: "ghcr.io/acme/tiny:v1",
			expectedTarget: "docker.io/myorg/acme/tiny:v1",
		},
		{
			name:           "digest reference is preserved",
			url:            testRegistryHost,
			repository:     "neutree",
			source:         "my-workload" + digest,
			expectedSource: "my-workload" + digest,
			expectedTarget: testRegistryHost + "/neutree/my-workload" + digest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			reg := testRegistry(tt.url, tt.repository, v1.ImageRegistryAuthConfig{Username: "user", Password: "pass"})
			lister := &fakeLister{withCreds: []v1.ImageRegistry{reg}}
			pusher := &fakePusher{}

			opts := baseOptions()
			opts.SourceImage = tt.source
			opts.Target = tt.target

			target, err := Push(context.Background(), lister, pusher, opts)
			require.NoError(t, err)

			assert.Equal(t, tt.expectedTarget, target)
			require.Len(t, pusher.pushed, 1)
			assert.Equal(t, tt.expectedSource, pusher.pushed[0].source)
			assert.Equal(t, tt.expectedTarget, pusher.pushed[0].target)
		})
	}
}

func TestPushUsesRegistryCredentials(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{credentialedRegistry()}}
	pusher := &fakePusher{}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.NoError(t, err)

	require.Len(t, pusher.pushed, 1)
	assert.Equal(t, expectedAuth(t), pusher.pushed[0].auth)
	require.Len(t, lister.calls, 1)
	assert.True(t, lister.calls[0].WithCreds)
}

func TestPushDecodesAuthField(t *testing.T) {
	reg := testRegistry(testRegistryHost, "neutree", v1.ImageRegistryAuthConfig{
		Auth: base64.StdEncoding.EncodeToString([]byte("user:pass")),
	})
	lister := &fakeLister{withCreds: []v1.ImageRegistry{reg}}
	pusher := &fakePusher{}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.NoError(t, err)

	require.Len(t, pusher.pushed, 1)
	assert.Equal(t, expectedAuth(t), pusher.pushed[0].auth)
}

func TestPushSkipsCredentialsFetchWhenFlagsGiven(t *testing.T) {
	lister := &fakeLister{masked: []v1.ImageRegistry{maskedRegistry()}}
	pusher := &fakePusher{}

	opts := baseOptions()
	opts.Username = "user"
	opts.Password = "pass"

	_, err := Push(context.Background(), lister, pusher, opts)
	require.NoError(t, err)

	require.Len(t, lister.calls, 1, "explicit credentials must not fetch the stored secret")
	assert.False(t, lister.calls[0].WithCreds)
	require.Len(t, pusher.pushed, 1)
	assert.Equal(t, expectedAuth(t), pusher.pushed[0].auth)
}

func TestPushTrimsIdentifierOptions(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{credentialedRegistry()}}
	pusher := &fakePusher{}

	target, err := Push(context.Background(), lister, pusher, PushOptions{
		Workspace:   " default ",
		Registry:    " default ",
		SourceImage: " my-workload:v1 ",
		Target:      "   ",
	})
	require.NoError(t, err)

	assert.Equal(t, testRegistryHost+"/neutree/my-workload:v1", target)
	require.Len(t, lister.calls, 1)
	assert.Equal(t, "default", lister.calls[0].Workspace)
	assert.Equal(t, "default", lister.calls[0].Name)
}

func TestPushDoesNotFallBackOnServerError(t *testing.T) {
	lister := &fakeLister{
		withCredsErr: apiError(http.StatusInternalServerError),
		masked:       []v1.ImageRegistry{maskedRegistry()},
	}
	pusher := &fakePusher{}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "500")
	assert.Len(t, lister.calls, 1, "must not retry against the masked endpoint")
	assert.Empty(t, pusher.pushed)
}

func TestPushFallsBackOnCredentialPermissionErrors(t *testing.T) {
	for _, status := range []int{http.StatusUnauthorized, http.StatusForbidden, http.StatusNotFound} {
		t.Run(http.StatusText(status), func(t *testing.T) {
			lister := &fakeLister{
				withCredsErr: apiError(status),
				masked:       []v1.ImageRegistry{maskedRegistry()},
			}
			pusher := &fakePusher{}

			target, err := Push(context.Background(), lister, pusher, baseOptions())
			require.NoError(t, err)

			assert.Equal(t, testRegistryHost+"/neutree/my-workload:v1", target)
			require.Len(t, lister.calls, 2)
			assert.True(t, lister.calls[0].WithCreds)
			assert.False(t, lister.calls[1].WithCreds)
			require.Len(t, pusher.pushed, 1)
			assert.Empty(t, pusher.pushed[0].auth, "masked read exposes no credentials")
		})
	}
}

func TestPushAnonymouslyWhenRegistryHasNoCredentials(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{maskedRegistry()}}
	pusher := &fakePusher{pushErr: errors.New("no basic auth credentials")}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.Error(t, err)

	require.Len(t, pusher.pushed, 1)
	assert.Empty(t, pusher.pushed[0].auth)
	assert.NotContains(t, err.Error(), "read-credentials",
		"a registry that simply stores no credentials must not be blamed on API key permissions")
}

func TestPushAnonymousFailureHintsAtCredentialsOnlyAfterFallback(t *testing.T) {
	lister := &fakeLister{
		withCredsErr: apiError(http.StatusForbidden),
		masked:       []v1.ImageRegistry{maskedRegistry()},
	}
	pusher := &fakePusher{pushErr: errors.New("unauthorized")}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "image_registry:read-credentials")
	assert.Contains(t, err.Error(), "unauthorized")
}

func TestPushLoadsArchiveBeforePushing(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{credentialedRegistry()}}
	pusher := &fakePusher{}

	opts := baseOptions()
	opts.ArchivePath = "/tmp/my-workload.tar"

	_, err := Push(context.Background(), lister, pusher, opts)
	require.NoError(t, err)

	assert.Equal(t, []string{"/tmp/my-workload.tar"}, pusher.loaded)
	assert.Len(t, pusher.pushed, 1)
}

func TestPushArchiveLoadFailureSkipsPush(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{credentialedRegistry()}}
	pusher := &fakePusher{loadErr: errors.New("bad archive")}

	opts := baseOptions()
	opts.ArchivePath = "/tmp/broken.tar"

	_, err := Push(context.Background(), lister, pusher, opts)
	require.Error(t, err)
	assert.Empty(t, pusher.pushed)
}

func TestPushInvalidTargetFailsBeforeLoadingArchive(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{credentialedRegistry()}}
	pusher := &fakePusher{}

	opts := baseOptions()
	opts.Target = "NOT A REFERENCE"
	opts.ArchivePath = "/tmp/my-workload.tar"

	_, err := Push(context.Background(), lister, pusher, opts)
	require.Error(t, err)
	assert.Contains(t, err.Error(), "is not valid")
	assert.Empty(t, pusher.loaded, "a bad reference must not cost a multi-gigabyte archive load")
	assert.Empty(t, pusher.pushed)
}

func TestPushRegistryNotFound(t *testing.T) {
	lister := &fakeLister{withCreds: []v1.ImageRegistry{}, masked: []v1.ImageRegistry{}}
	pusher := &fakePusher{}

	_, err := Push(context.Background(), lister, pusher, baseOptions())
	require.Error(t, err)
	assert.Contains(t, err.Error(), "not found in workspace default")
}

func TestPushValidation(t *testing.T) {
	tests := []struct {
		name    string
		mutate  func(*PushOptions)
		wantErr string
	}{
		{
			name:    "missing image",
			mutate:  func(o *PushOptions) { o.SourceImage = "  " },
			wantErr: "image reference is required",
		},
		{
			name:    "missing registry",
			mutate:  func(o *PushOptions) { o.Registry = "" },
			wantErr: "image registry name is required",
		},
		{
			name:    "username without password",
			mutate:  func(o *PushOptions) { o.Username = "user" },
			wantErr: "must be set together",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			opts := baseOptions()
			tt.mutate(&opts)

			_, err := Push(context.Background(), &fakeLister{}, &fakePusher{}, opts)
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestNormalizeReference(t *testing.T) {
	tests := []struct {
		in       string
		expected string
	}{
		{"my-workload", "my-workload:latest"},
		{"my-workload:v1", "my-workload:v1"},
		{"ghcr.io/acme/my-workload", "ghcr.io/acme/my-workload:latest"},
		{"registry:5000/acme/my-workload", "registry:5000/acme/my-workload:latest"},
		{"registry:5000/acme/my-workload:v1", "registry:5000/acme/my-workload:v1"},
	}

	for _, tt := range tests {
		t.Run(tt.in, func(t *testing.T) {
			assert.Equal(t, tt.expected, normalizeReference(tt.in))
		})
	}
}
