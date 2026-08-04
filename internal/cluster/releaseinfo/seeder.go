package releaseinfo

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Store is the narrow persistence boundary used by the API startup seed path.
type Store interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
	CreateReleaseInfo(*v1.ReleaseInfo) error
	UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error
}

// SynchronizeSeed builds the current release seed, evaluates its allowed
// transition, and persists only an insert, update, or stable promotion.
func SynchronizeSeed(store Store, buildIdentity string) (SyncResult, error) {
	candidate, err := NewSeed(buildIdentity)
	if err != nil {
		return SyncResult{}, err
	}

	releases, err := store.ListReleaseInfo()
	if err != nil {
		return SyncResult{}, fmt.Errorf("list release infos: %w", err)
	}

	var existing *v1.ReleaseInfo

	for index := range releases {
		if releases[index].GetName() == candidate.GetName() {
			existing = &releases[index]
			break
		}
	}

	result, err := Synchronize(existing, releases, candidate)
	if err != nil {
		return SyncResult{}, err
	}

	switch result.Action {
	case SyncActionInsert:
		if err := store.CreateReleaseInfo(copyReleaseInfo(result.Desired)); err != nil {
			return SyncResult{}, fmt.Errorf("create release info: %w", err)
		}
	case SyncActionUpdate, SyncActionPromote:
		if err := store.UpdateReleaseInfo(existing.GetID(), copyReleaseInfo(result.Desired)); err != nil {
			return SyncResult{}, fmt.Errorf("update release info: %w", err)
		}
	}

	return result, nil
}

func copyReleaseInfo(info *v1.ReleaseInfo) *v1.ReleaseInfo {
	copy := *info
	return &copy
}
