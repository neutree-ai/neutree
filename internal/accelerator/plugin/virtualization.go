package plugin

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
)

type VirtualizationNodeScopeLabel struct {
	Key           string
	EnabledValue  string
	DisabledValue string
}

type GPUOperatorClusterPolicy struct {
	Name string
	Spec map[string]interface{}
}

type VirtualizationConfigInput struct {
	Nodes                      []corev1.Node
	GPUOperatorClusterPolicies []GPUOperatorClusterPolicy
}

type VirtualizationConfig struct {
	Supported       bool
	BlockingReasons []string
	CandidateNodes  []string
	NodeScopeLabel  VirtualizationNodeScopeLabel
	ConfigPatch     map[string]interface{}
}

type VirtualizationConfigResolver interface {
	ResolveVirtualizationConfig(ctx context.Context, input VirtualizationConfigInput) (*VirtualizationConfig, error)
}

func NewUnsupportedVirtualizationConfig(acceleratorType string) *VirtualizationConfig {
	return &VirtualizationConfig{
		Supported: false,
		BlockingReasons: []string{
			fmt.Sprintf("accelerator %s does not support HAMi virtualization", acceleratorType),
		},
	}
}
