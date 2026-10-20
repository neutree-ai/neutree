package component

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
