package releaseinfo

import (
	"fmt"
	"reflect"
	"strings"

	"github.com/Masterminds/semver/v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type SyncAction string

const (
	SyncActionInsert   SyncAction = "Insert"
	SyncActionUpdate   SyncAction = "Update"
	SyncActionPromote  SyncAction = "Promote"
	SyncActionNoop     SyncAction = "Noop"
	SyncActionReadOnly SyncAction = "ReadOnly"
)

type SyncResult struct {
	Action  SyncAction
	Desired *v1.ReleaseInfo
}

// NormalizeControlPlaneRelease returns the ReleaseInfo key and channel derived
// from a complete control-plane build identity.
func NormalizeControlPlaneRelease(buildIdentity string) (string, v1.ReleaseInfoChannel, error) {
	identity := strings.TrimSpace(buildIdentity)
	if !strings.HasPrefix(identity, "v") {
		return "", "", fmt.Errorf("control-plane release %q must use v-prefixed semantic version", buildIdentity)
	}

	version, err := semver.StrictNewVersion(strings.TrimPrefix(identity, "v"))
	if err != nil {
		return "", "", fmt.Errorf("invalid control-plane release %q: %w", buildIdentity, err)
	}

	baseline := fmt.Sprintf("v%d.%d.%d", version.Major(), version.Minor(), version.Patch())
	if identity == baseline {
		return baseline, v1.ReleaseInfoChannelStable, nil
	}

	return baseline, v1.ReleaseInfoChannelNightly, nil
}

// Synchronize evaluates the only allowed ReleaseInfo seed transitions. Storage
// callers persist Desired only for Insert, Update, and Promote actions.
func Synchronize(existing *v1.ReleaseInfo, historicalStable []v1.ReleaseInfo, candidate *v1.ReleaseInfo) (SyncResult, error) {
	if err := validateReleaseInfo(candidate); err != nil {
		return SyncResult{}, err
	}

	if existing == nil {
		if err := validatePublishedClusterVersionMatrices(candidate, historicalStable); err != nil {
			return SyncResult{}, err
		}

		return SyncResult{Action: SyncActionInsert, Desired: candidate}, nil
	}

	if err := validateReleaseInfo(existing); err != nil {
		return SyncResult{}, fmt.Errorf("invalid stored release info: %w", err)
	}

	if existing.GetName() != candidate.GetName() {
		return SyncResult{}, fmt.Errorf("release info baseline mismatch: %q != %q", existing.GetName(), candidate.GetName())
	}

	if existing.Spec.Channel == v1.ReleaseInfoChannelStable {
		if candidate.Spec.Channel == v1.ReleaseInfoChannelStable {
			if reflect.DeepEqual(existing.Spec, candidate.Spec) {
				return SyncResult{Action: SyncActionNoop, Desired: existing}, nil
			}

			return SyncResult{}, fmt.Errorf("stable release info differs for baseline %q", existing.GetName())
		}

		return SyncResult{Action: SyncActionReadOnly, Desired: existing}, nil
	}

	if candidate.Spec.Channel == v1.ReleaseInfoChannelStable {
		if err := validatePublishedClusterVersionMatrices(candidate, historicalStable); err != nil {
			return SyncResult{}, err
		}

		return SyncResult{Action: SyncActionPromote, Desired: candidate}, nil
	}

	comparison, err := compareBuildIdentity(candidate.Spec.BuildIdentity, existing.Spec.BuildIdentity)
	if err != nil {
		return SyncResult{}, err
	}

	switch {
	case comparison > 0:
		return SyncResult{Action: SyncActionUpdate, Desired: candidate}, nil
	case comparison == 0:
		return SyncResult{Action: SyncActionNoop, Desired: existing}, nil
	default:
		return SyncResult{Action: SyncActionReadOnly, Desired: existing}, nil
	}
}

func validateReleaseInfo(info *v1.ReleaseInfo) error {
	if info == nil || info.Metadata == nil || info.Spec == nil {
		return fmt.Errorf("release info, metadata, and spec are required")
	}

	baseline, channel, err := NormalizeControlPlaneRelease(info.Spec.BuildIdentity)
	if err != nil {
		return err
	}

	if info.Metadata.Name != baseline {
		return fmt.Errorf("release info name %q must match baseline %q", info.Metadata.Name, baseline)
	}

	if info.Spec.Channel != channel {
		return fmt.Errorf("release info channel %q must match build identity channel %q", info.Spec.Channel, channel)
	}

	return nil
}

func validatePublishedClusterVersionMatrices(candidate *v1.ReleaseInfo, historicalStable []v1.ReleaseInfo) error {
	if candidate.Spec.Channel != v1.ReleaseInfoChannelStable {
		return nil
	}

	for historicalIndex := range historicalStable {
		historical := &historicalStable[historicalIndex]
		if historical.Spec == nil || historical.Spec.Channel != v1.ReleaseInfoChannelStable {
			continue
		}

		for _, candidateVersion := range candidate.Spec.ClusterVersions {
			for _, historicalVersion := range historical.Spec.ClusterVersions {
				if candidateVersion.Version != historicalVersion.Version {
					continue
				}

				if !reflect.DeepEqual(candidateVersion, historicalVersion) {
					return fmt.Errorf("published component matrix differs for cluster version %q", candidateVersion.Version)
				}
			}
		}
	}

	return nil
}

func compareBuildIdentity(candidate, existing string) (int, error) {
	candidateVersion, err := semver.NewVersion(candidate)
	if err != nil {
		return 0, fmt.Errorf("invalid candidate build identity %q: %w", candidate, err)
	}

	existingVersion, err := semver.NewVersion(existing)
	if err != nil {
		return 0, fmt.Errorf("invalid stored build identity %q: %w", existing, err)
	}

	switch {
	case candidateVersion.GreaterThan(existingVersion):
		return 1, nil
	case candidateVersion.LessThan(existingVersion):
		return -1, nil
	default:
		return 0, nil
	}
}
