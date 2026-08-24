package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// SynchronizeCurrentBaseline makes the process Catalog authoritative for its
// current ReleaseInfo and exact ClusterProfiles.
func SynchronizeCurrentBaseline(
	releaseInfoStorage storage.ReleaseInfoStorage,
	clusterProfileStorage storage.ClusterProfileStorage,
) error {
	builder := NewBuilder()
	baseline := builder.CurrentReleaseInfoBaseline()

	info, err := builder.BuildReleaseInfo(baseline)
	if err != nil {
		return fmt.Errorf("build release info: %w", err)
	}

	profiles, err := builder.BuildClusterProfiles(baseline)
	if err != nil {
		return fmt.Errorf("build cluster profile catalog: %w", err)
	}

	infos, err := releaseInfoStorage.ListReleaseInfo()
	if err != nil {
		return fmt.Errorf("list release infos: %w", err)
	}

	persistedProfiles, err := clusterProfileStorage.ListClusterProfile(storage.ListOption{})
	if err != nil {
		return fmt.Errorf("list cluster profiles: %w", err)
	}

	existingProfiles, err := clusterProfileIndexByName(persistedProfiles)
	if err != nil {
		return err
	}

	if existing := releaseInfoByName(infos, baseline); existing == nil {
		if err := releaseInfoStorage.CreateReleaseInfo(cloneReleaseInfo(info)); err != nil {
			return fmt.Errorf("create release info: %w", err)
		}
	} else {
		if existing.GetID() == "" || existing.GetID() == "0" {
			return fmt.Errorf("persisted release info %q has no identifier", baseline)
		}

		if err := releaseInfoStorage.UpdateReleaseInfo(existing.GetID(), cloneReleaseInfo(info)); err != nil {
			return fmt.Errorf("update release info: %w", err)
		}
	}

	for _, profile := range profiles {
		existing := existingProfiles[profile.GetName()]
		if existing == nil {
			if err := clusterProfileStorage.CreateClusterProfile(cloneClusterProfile(profile)); err != nil {
				return fmt.Errorf("create cluster profile %s: %w", profile.GetName(), err)
			}

			continue
		}

		if existing.GetID() == "" || existing.GetID() == "0" {
			return fmt.Errorf("persisted cluster profile %q has no identifier", profile.GetName())
		}

		if err := clusterProfileStorage.UpdateClusterProfile(existing.GetID(), cloneClusterProfile(profile)); err != nil {
			return fmt.Errorf("update cluster profile %s: %w", profile.GetName(), err)
		}
	}

	return nil
}

func releaseInfoByName(infos []v1.ReleaseInfo, name string) *v1.ReleaseInfo {
	for index := range infos {
		if infos[index].GetName() == name {
			return &infos[index]
		}
	}

	return nil
}

func clusterProfileIndexByName(profiles []v1.ClusterProfile) (map[string]*v1.ClusterProfile, error) {
	indexed := make(map[string]*v1.ClusterProfile, len(profiles))
	for index := range profiles {
		name := profiles[index].GetName()
		if _, found := indexed[name]; found {
			return nil, fmt.Errorf("duplicate persisted cluster profile %q", name)
		}

		indexed[name] = &profiles[index]
	}

	return indexed, nil
}
