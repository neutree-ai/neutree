package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// CurrentBaselineStore is the narrow write boundary used by Core startup.
type CurrentBaselineStore interface {
	ListReleaseInfo() ([]v1.ReleaseInfo, error)
	CreateReleaseInfo(*v1.ReleaseInfo) error
	UpdateReleaseInfo(id string, info *v1.ReleaseInfo) error
	ListClusterProfile() ([]v1.ClusterProfile, error)
	CreateClusterProfile(*v1.ClusterProfile) error
}

// SynchronizeCurrentBaseline updates the running control-plane ReleaseInfo and
// creates missing exact Profiles. Existing Profiles are immutable: an
// identical replay is a no-op and any content drift fails before a write.
func SynchronizeCurrentBaseline(store CurrentBaselineStore, baseline string, builder Builder) error {
	if store == nil {
		return fmt.Errorf("current baseline store is required")
	}

	if builder == nil {
		return fmt.Errorf("release profile builder is required")
	}

	info, err := builder.BuildReleaseInfo(baseline)
	if err != nil {
		return fmt.Errorf("build release info: %w", err)
	}

	if err := ValidateReleaseInfo(info); err != nil {
		return fmt.Errorf("invalid release info builder output: %w", err)
	}

	if info.GetName() != baseline {
		return fmt.Errorf("release info builder output name %q must match requested baseline %q", info.GetName(), baseline)
	}

	profiles, err := builder.BuildClusterProfiles(baseline)
	if err != nil {
		return fmt.Errorf("build cluster profile catalog: %w", err)
	}

	if err := validateCurrentClusterProfiles(info, profiles); err != nil {
		return err
	}

	infos, err := store.ListReleaseInfo()
	if err != nil {
		return fmt.Errorf("list release infos: %w", err)
	}

	persistedProfiles, err := store.ListClusterProfile()
	if err != nil {
		return fmt.Errorf("list cluster profiles: %w", err)
	}

	existingProfiles, err := clusterProfileIndexByName(persistedProfiles)
	if err != nil {
		return err
	}

	for _, profile := range profiles {
		existing := existingProfiles[profile.GetName()]
		if existing == nil {
			continue
		}

		if !ClusterProfilesSemanticallyEqual(existing, profile) {
			return fmt.Errorf("cluster profile %s content drift", profile.GetName())
		}
	}

	if existing := releaseInfoByName(infos, baseline); existing == nil {
		if err := store.CreateReleaseInfo(cloneReleaseInfo(info)); err != nil {
			return fmt.Errorf("create release info: %w", err)
		}
	} else {
		if existing.GetID() == "" || existing.GetID() == "0" {
			return fmt.Errorf("persisted release info %q has no identifier", baseline)
		}

		if err := store.UpdateReleaseInfo(existing.GetID(), cloneReleaseInfo(info)); err != nil {
			return fmt.Errorf("update release info: %w", err)
		}
	}

	for _, profile := range profiles {
		if existingProfiles[profile.GetName()] != nil {
			continue
		}

		if err := store.CreateClusterProfile(cloneClusterProfile(profile)); err != nil {
			return fmt.Errorf("create cluster profile %s: %w", profile.GetName(), err)
		}
	}

	return nil
}

func validateCurrentClusterProfiles(info *v1.ReleaseInfo, profiles []*v1.ClusterProfile) error {
	if len(profiles) == 0 {
		return fmt.Errorf("cluster profile catalog is empty")
	}

	seen := make(map[string]struct{}, len(profiles))
	for _, profile := range profiles {
		if err := ValidateProfileEligibility(info, profile); err != nil {
			return fmt.Errorf("invalid cluster profile builder output: %w", err)
		}

		name := profile.GetName()
		if _, found := seen[name]; found {
			return fmt.Errorf("duplicate cluster profile builder output %q", name)
		}

		seen[name] = struct{}{}
	}

	if _, found := seen[info.Spec.DefaultClusterVersion]; !found {
		return fmt.Errorf("cluster profile catalog is missing default cluster version %q", info.Spec.DefaultClusterVersion)
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
