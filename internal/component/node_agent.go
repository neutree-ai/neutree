package component

import (
	"strings"

	"github.com/neutree-ai/neutree/internal/semver"
)

// NodeAgentContract is the parameter surface the control plane renders for a
// NodeAgent. NodeAgent releases differ in the flags and environment they
// accept, so the contract is pinned to the cluster version that selects it
// rather than negotiated at runtime.
type NodeAgentContract string

const (
	// NodeAgentContractLegacy is the surface NodeAgent up to v1.1.1 accepts: the
	// legacy cluster type with an explicit metrics mode and no accelerator target
	// flags.
	NodeAgentContractLegacy NodeAgentContract = "legacy"

	// NodeAgentContractProfile is the surface NodeAgent v1.1.2 and newer accepts:
	// the per-backend cluster type, the accelerator target flags, and the
	// environment the selected accelerator profile declares.
	NodeAgentContractProfile NodeAgentContract = "profile"
)

type NodeAgentSelection struct {
	Contract NodeAgentContract
	Image    string
}

// nodeAgentTier is one rung of the NodeAgent version ladder.
type nodeAgentTier struct {
	// maxVersion is the last cluster version in the tier. Empty means the tier is
	// open ended and covers every newer version.
	maxVersion string
	// contract is the parameter surface the image in this tier accepts.
	contract NodeAgentContract
	image    string
}

// nodeAgentTiers is the ladder, oldest tier first. A cluster version picks the
// first tier it does not exceed, so the cluster version alone decides both the
// CLI contract and the image; an accelerator profile has no say.
//
// The last tier is open ended, so shipping a new image is a one line change to
// NeutreeNodeAgent in version.go and nothing here moves.
var nodeAgentTiers = []nodeAgentTier{
	{
		maxVersion: "v1.1.1",
		contract:   NodeAgentContractLegacy,
		image:      nodeAgentImage(LegacyNeutreeNodeAgent),
	},
	{
		maxVersion: "v1.2.0",
		contract:   NodeAgentContractProfile,
		image:      nodeAgentImage(NeutreeNodeAgentV120),
	},
	{
		contract: NodeAgentContractProfile,
		image:    nodeAgentImage(NeutreeNodeAgent),
	},
}

// SelectNodeAgent resolves the CLI contract and image for a cluster version.
func SelectNodeAgent(version string) (NodeAgentSelection, error) {
	tier, err := pickNodeAgentTier(version)
	if err != nil {
		return NodeAgentSelection{}, err
	}

	return NodeAgentSelection{
		Contract: tier.contract,
		Image:    tier.image,
	}, nil
}

func pickNodeAgentTier(version string) (nodeAgentTier, error) {
	version = strings.TrimSpace(version)
	if version == "" {
		// A cluster without a version predates every tier.
		return nodeAgentTiers[0], nil
	}

	baseVersion, err := semver.BaseVersion(version)
	if err != nil {
		return nodeAgentTier{}, err
	}

	for _, tier := range nodeAgentTiers {
		if tier.maxVersion == "" {
			return tier, nil
		}

		exceedsTier, err := semver.LessThan(tier.maxVersion, baseVersion)
		if err != nil {
			return nodeAgentTier{}, err
		}

		if !exceedsTier {
			return tier, nil
		}
	}

	// nodeAgentTiers always ends with an open ended tier.
	return nodeAgentTiers[len(nodeAgentTiers)-1], nil
}

func nodeAgentImage(version string) string {
	return "neutree/neutree-node-agent:" + version
}
