package releaseinfo

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNormalizeClusterMinor(t *testing.T) {
	testCases := []struct {
		name    string
		version string
		want    string
		wantErr string
	}{
		{name: "stable", version: "v1.2.0", want: "v1.2"},
		{name: "alpha", version: "v1.2.0-alpha.1", want: "v1.2"},
		{name: "release candidate", version: "v1.2.0-rc.1", want: "v1.2"},
		{name: "requires v prefix", version: "1.2.0", wantErr: "v-prefixed"},
		{name: "rejects incomplete semantic version", version: "v1.2", wantErr: "invalid cluster version"},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			minor, err := NormalizeClusterMinor(testCase.version)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, minor)
		})
	}
}

func TestResolveCurrentControlPlaneBaseline(t *testing.T) {
	testCases := []struct {
		name     string
		identity string
		infos    []v1.ReleaseInfo
		want     string
		wantErr  string
	}{
		{name: "stable", identity: "v1.2.0", want: "v1.2.0"},
		{name: "nightly", identity: "v1.2.0-nightly.20260805", want: "v1.2.0"},
		{name: "alpha", identity: "v1.2.0-alpha.1", want: "v1.2.0"},
		{name: "release candidate", identity: "v1.2.0-rc.1", want: "v1.2.0"},
		{
			name:     "development build uses highest persisted release info",
			identity: "v1.2.0-dev.12",
			infos: []v1.ReleaseInfo{
				*releaseInfoNamed("v1.1.1"),
				*releaseInfoNamed("not-a-version"),
				*releaseInfoNamed("v1.3.0"),
				*releaseInfoNamed("v1.2.0"),
			},
			want: "v1.3.0",
		},
		{
			name:     "dirty build uses highest persisted release info",
			identity: "v1.2.0-dirty",
			infos: []v1.ReleaseInfo{
				*releaseInfoNamed("v1.2.0-alpha.2"),
				*releaseInfoNamed("v1.2.0"),
			},
			want: "v1.2.0",
		},
		{
			name:     "raw development marker uses highest persisted release info",
			identity: "dev",
			infos:    []v1.ReleaseInfo{*releaseInfoNamed("v1.4.0")},
			want:     "v1.4.0",
		},
		{
			name:     "no valid persisted release info",
			identity: "dirty",
			infos: []v1.ReleaseInfo{
				*releaseInfoNamed("not-a-version"),
				{Metadata: nil},
			},
			wantErr: "no valid persisted release info",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			baseline, err := ResolveCurrentControlPlaneBaseline(testCase.identity, testCase.infos)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.want, baseline)
		})
	}
}

func TestProviderResolvesDevelopmentBuildToHighestPersistedBaseline(t *testing.T) {
	provider := NewProvider(&releaseInfoReader{infos: []v1.ReleaseInfo{
		*releaseInfoNamed("v1.1.1"),
		*releaseInfoNamed("v1.2.0"),
	}}, "v1.2.0-dev.1")

	info, err := provider.Current()
	require.NoError(t, err)
	assert.Equal(t, "v1.2.0", info.GetName())
}

func releaseInfoNamed(name string) *v1.ReleaseInfo {
	return &v1.ReleaseInfo{Metadata: &v1.Metadata{Name: name}, Spec: &v1.ReleaseInfoSpec{}}
}
