package controllers

import (
	"fmt"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/cluster"
)

func (controller *ClusterController) ensureClusterReleaseCompatibility(c *v1.Cluster) (v1.ClusterPhase, error) {
	releaseAware, err := cluster.IsReleaseInfoAwareClusterVersion(c.GetVersion())
	if err != nil {
		controller.setReleaseCompatibility(c, nil, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())
		return v1.ClusterPhaseUnsupported, err
	}

	if !releaseAware {
		return "", nil
	}

	if controller.releaseInfoProvider == nil {
		err := errors.New("release info provider is required")
		controller.setReleaseCompatibility(c, nil, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())

		return v1.ClusterPhaseUnsupported, err
	}

	info, err := controller.releaseInfoProvider.Current()
	if err != nil {
		controller.setReleaseCompatibility(c, nil, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())

		return v1.ClusterPhaseUnsupported, err
	}

	if info == nil || info.Metadata == nil || info.Spec == nil || info.Status == nil {
		err := errors.New("release info metadata, spec, and status are required")
		controller.setReleaseCompatibility(c, info, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())

		return v1.ClusterPhaseUnsupported, err
	}

	version := releaseInfoClusterVersion(info, c.GetVersion())
	if version == nil {
		err := fmt.Errorf("cluster version %s is not supported by release info %s", c.GetVersion(), info.Metadata.Name)
		controller.setReleaseCompatibility(c, info, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())

		return v1.ClusterPhaseUnsupported, err
	}

	if version.State == v1.ReleaseInfoClusterVersionStateRetired {
		err := fmt.Errorf("cluster version %s is retired by release info %s", c.GetVersion(), info.Metadata.Name)
		controller.setReleaseCompatibility(c, info, v1.ClusterReleaseCompatibilityStateRetired, err.Error())

		return v1.ClusterPhaseRetired, err
	}

	if version.State != v1.ReleaseInfoClusterVersionStateActive {
		err := fmt.Errorf("cluster version %s has unsupported state %s", c.GetVersion(), version.State)
		controller.setReleaseCompatibility(c, info, v1.ClusterReleaseCompatibilityStateUnsupported, err.Error())

		return v1.ClusterPhaseUnsupported, err
	}

	controller.setReleaseCompatibility(c, info, v1.ClusterReleaseCompatibilityStateCompatible, "")

	return "", nil
}

func releaseInfoClusterVersion(info *v1.ReleaseInfo, want string) *v1.ReleaseInfoClusterVersion {
	if info == nil || info.Spec == nil {
		return nil
	}

	for index := range info.Spec.ClusterVersions {
		if info.Spec.ClusterVersions[index].Version == want {
			return &info.Spec.ClusterVersions[index]
		}
	}

	return nil
}

func (controller *ClusterController) setReleaseCompatibility(c *v1.Cluster, info *v1.ReleaseInfo,
	state v1.ClusterReleaseCompatibilityState, reason string) {
	if c.Status == nil {
		c.Status = &v1.ClusterStatus{}
	}

	if info != nil && info.Metadata != nil && info.Status != nil {
		c.Status.ReleaseInfo = &v1.ReleaseInfoReference{
			Baseline: info.Metadata.Name,
			Revision: info.Status.Revision,
		}
	}

	effectiveVersion := c.Status.Version
	if effectiveVersion == "" {
		effectiveVersion = c.GetVersion()
	}

	c.Status.ReleaseCompatibility = &v1.ClusterReleaseCompatibility{
		EffectiveVersion: effectiveVersion,
		ResolvedVersion:  c.GetVersion(),
		State:            state,
		Reason:           reason,
	}
}
