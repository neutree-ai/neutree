package plugin

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	"github.com/neutree-ai/neutree/pkg/accelerator"
)

type GPUOperatorClusterPolicy struct {
	Name string
	Spec map[string]interface{}
}

type VirtualizationConfigInput struct {
	Nodes                      []corev1.Node
	GPUOperatorClusterPolicies []GPUOperatorClusterPolicy
}

type VirtualizationConfigResolver interface {
	ResolveVirtualizationConfig(ctx context.Context, input VirtualizationConfigInput) (*accelerator.VirtualizationConfig, error)
}

func NewUnsupportedVirtualizationConfig(acceleratorType string) *accelerator.VirtualizationConfig {
	return &accelerator.VirtualizationConfig{
		Supported: false,
		BlockingReasons: []string{
			fmt.Sprintf("accelerator %s does not support HAMi virtualization", acceleratorType),
		},
	}
}
