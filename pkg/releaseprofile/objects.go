package releaseprofile

import (
	"maps"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func cloneReleaseInfo(info *v1.ReleaseInfo) *v1.ReleaseInfo {
	if info == nil {
		return nil
	}

	copy := *info
	copy.Metadata = cloneMetadata(info.Metadata)
	if info.Spec != nil {
		spec := *info.Spec
		spec.CompatibleClusterBaselines = append([]string(nil), info.Spec.CompatibleClusterBaselines...)
		copy.Spec = &spec
	}

	return &copy
}

func cloneClusterProfile(profile *v1.ClusterProfile) *v1.ClusterProfile {
	if profile == nil {
		return nil
	}

	copy := *profile
	copy.Metadata = cloneMetadata(profile.Metadata)
	if profile.Spec != nil {
		spec := *profile.Spec
		spec.Components = maps.Clone(profile.Spec.Components)
		copy.Spec = &spec
	}

	return &copy
}

func cloneMetadata(metadata *v1.Metadata) *v1.Metadata {
	if metadata == nil {
		return nil
	}

	copy := *metadata
	copy.Labels = maps.Clone(metadata.Labels)
	copy.Annotations = maps.Clone(metadata.Annotations)

	return &copy
}
