package component

import (
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/semver"
)

const ProfileImageVersionGate = "v1.1.1"

type NodeAgentContract string

const (
	NodeAgentContractLegacy  NodeAgentContract = "legacy"
	NodeAgentContractProfile NodeAgentContract = "profile"
)

type NodeAgentSelection struct {
	Contract NodeAgentContract
	Image    string
}

// SelectNodeAgent resolves the CLI contract and image that a cluster version
// supports. Newer clusters use a supplied profile image when present.
func SelectNodeAgent(version string, profile *v1.NodeAgentRuntimeProfile) (NodeAgentSelection, error) {
	supportsProfile, err := SupportsNodeAgentProfileContract(version)
	if err != nil {
		return NodeAgentSelection{}, err
	}

	if !supportsProfile {
		return NodeAgentSelection{
			Contract: NodeAgentContractLegacy,
			Image:    defaultLegacyNodeAgentImage(),
		}, nil
	}

	if profile == nil {
		return NodeAgentSelection{
			Contract: NodeAgentContractProfile,
			Image:    defaultProfileNodeAgentImage(),
		}, nil
	}

	if strings.TrimSpace(profile.Image) == "" {
		return NodeAgentSelection{}, fmt.Errorf("node agent profile image is required for cluster versions newer than %s", ProfileImageVersionGate)
	}

	return NodeAgentSelection{
		Contract: NodeAgentContractProfile,
		Image:    profile.Image,
	}, nil
}

func SupportsNodeAgentProfileContract(version string) (bool, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		return false, nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return false, err
	}

	legacyOrOlder, err := semver.LessThan(baseVersion, "v1.1.2")
	if err != nil {
		return false, err
	}

	return !legacyOrOlder, nil
}

func defaultLegacyNodeAgentImage() string {
	return "neutree/neutree-node-agent:" + LegacyNeutreeNodeAgent
}

func defaultProfileNodeAgentImage() string {
	return "neutree/neutree-node-agent:" + NeutreeNodeAgent
}
