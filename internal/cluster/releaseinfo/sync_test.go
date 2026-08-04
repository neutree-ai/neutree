package releaseinfo

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestNormalizeControlPlaneRelease(t *testing.T) {
	testCases := []struct {
		name         string
		input        string
		wantBaseline string
		wantChannel  v1.ReleaseInfoChannel
		wantErr      string
	}{
		{
			name:         "stable release uses its exact baseline",
			input:        "v1.2.0",
			wantBaseline: "v1.2.0",
			wantChannel:  v1.ReleaseInfoChannelStable,
		},
		{
			name:         "nightly release uses its stable baseline",
			input:        "v1.2.0-nightly.20260804",
			wantBaseline: "v1.2.0",
			wantChannel:  v1.ReleaseInfoChannelNightly,
		},
		{
			name:         "release candidate uses its stable baseline",
			input:        "v1.2.0-rc.1",
			wantBaseline: "v1.2.0",
			wantChannel:  v1.ReleaseInfoChannelNightly,
		},
		{
			name:    "rejects invalid release identity",
			input:   "nightly-latest",
			wantErr: "must use v-prefixed semantic version",
		},
		{
			name:    "rejects stable release without v prefix",
			input:   "1.2.0",
			wantErr: "must use v-prefixed semantic version",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			baseline, channel, err := NormalizeControlPlaneRelease(testCase.input)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.wantBaseline, baseline)
			assert.Equal(t, testCase.wantChannel, channel)
		})
	}
}

func TestSynchronize(t *testing.T) {
	stableV110 := releaseInfo(v1.ReleaseInfoChannelStable, "v1.1.0", "v1.1.0")
	nightlyV1201 := releaseInfo(v1.ReleaseInfoChannelNightly, "v1.2.0-nightly.1", "v1.1.1")
	nightlyV1202 := releaseInfo(v1.ReleaseInfoChannelNightly, "v1.2.0-nightly.2", "v1.1.1")
	stableV120 := releaseInfo(v1.ReleaseInfoChannelStable, "v1.2.0", "v1.1.1")

	testCases := []struct {
		name         string
		existing     *v1.ReleaseInfo
		historical   []v1.ReleaseInfo
		candidate    *v1.ReleaseInfo
		wantAction   SyncAction
		wantExisting bool
		wantErr      string
	}{
		{
			name:       "inserts missing matrix",
			candidate:  stableV110,
			wantAction: SyncActionInsert,
		},
		{
			name:       "updates nightly only with newer build identity",
			existing:   nightlyV1201,
			candidate:  nightlyV1202,
			wantAction: SyncActionUpdate,
		},
		{
			name:         "same nightly build is a no-op",
			existing:     nightlyV1202,
			candidate:    nightlyV1202,
			wantAction:   SyncActionNoop,
			wantExisting: true,
		},
		{
			name:         "older nightly only reads",
			existing:     nightlyV1202,
			candidate:    nightlyV1201,
			wantAction:   SyncActionReadOnly,
			wantExisting: true,
		},
		{
			name:       "stable promotes same-baseline nightly once",
			existing:   nightlyV1202,
			candidate:  stableV120,
			wantAction: SyncActionPromote,
		},
		{
			name:         "matching stable seed is a no-op",
			existing:     stableV120,
			candidate:    stableV120,
			wantAction:   SyncActionNoop,
			wantExisting: true,
		},
		{
			name:     "stable mismatch fails",
			existing: stableV120,
			candidate: releaseInfo(
				v1.ReleaseInfoChannelStable,
				"v1.2.0",
				"v1.2.0",
			),
			wantErr: "stable release info differs",
		},
		{
			name:         "nightly cannot overwrite stable",
			existing:     stableV120,
			candidate:    nightlyV1202,
			wantAction:   SyncActionReadOnly,
			wantExisting: true,
		},
		{
			name:       "later stable reuses published component matrix",
			historical: []v1.ReleaseInfo{*stableV110},
			candidate:  releaseInfo(v1.ReleaseInfoChannelStable, "v1.2.0", "v1.1.0"),
			wantAction: SyncActionInsert,
		},
		{
			name:       "later stable cannot replace published component matrix",
			historical: []v1.ReleaseInfo{*stableV110},
			candidate:  releaseInfo(v1.ReleaseInfoChannelStable, "v1.2.0", "v1.1.1"),
			wantErr:    "published component matrix differs",
		},
		{
			name:       "later stable cannot change published upgrade matrix",
			historical: []v1.ReleaseInfo{*stableV110},
			candidate: func() *v1.ReleaseInfo {
				candidate := releaseInfo(v1.ReleaseInfoChannelStable, "v1.2.0", "v1.1.0")
				candidate.Spec.ClusterVersions[0].UpgradeTo = []string{"v1.2.0"}
				return candidate
			}(),
			wantErr: "published component matrix differs",
		},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			result, err := Synchronize(testCase.existing, testCase.historical, testCase.candidate)
			if testCase.wantErr != "" {
				require.ErrorContains(t, err, testCase.wantErr)
				return
			}

			require.NoError(t, err)
			assert.Equal(t, testCase.wantAction, result.Action)
			if testCase.wantExisting {
				assert.Same(t, testCase.existing, result.Desired)
			} else {
				assert.Same(t, testCase.candidate, result.Desired)
			}
		})
	}
}

func releaseInfo(channel v1.ReleaseInfoChannel, buildIdentity, rayRuntime string) *v1.ReleaseInfo {
	baseline, _, err := NormalizeControlPlaneRelease(buildIdentity)
	if err != nil {
		panic(err)
	}

	return &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: baseline},
		Spec: &v1.ReleaseInfoSpec{
			Channel:       channel,
			BuildIdentity: buildIdentity,
			ClusterVersions: []v1.ReleaseInfoClusterVersion{
				{
					Version: "v1.1.0",
					State:   v1.ReleaseInfoClusterVersionStateActive,
					Components: map[string]string{
						"ray_runtime": rayRuntime,
						"router":      "neutree/router:" + strings.TrimPrefix(rayRuntime, "neutree/neutree-serve:"),
					},
					AcceleratorComponents: map[string]map[string]string{
						"nvidia_gpu": {
							"dcgm_exporter": "nvcr.io/nvidia/k8s/dcgm-exporter:4.5.3-4.8.2-distroless",
						},
					},
				},
			},
		},
		Status: &v1.ReleaseInfoStatus{Revision: buildIdentity},
	}
}
