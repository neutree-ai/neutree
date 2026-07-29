package plugin

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"

	v1 "github.com/neutree-ai/neutree/api/v1"
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
	ResolveVirtualizationConfig(ctx context.Context, input VirtualizationConfigInput) (*v1.VirtualizationConfig, error)
}

func NewUnsupportedVirtualizationConfig(acceleratorType string) *v1.VirtualizationConfig {
	return &v1.VirtualizationConfig{
		Supported: false,
		BlockingReasons: []string{
			fmt.Sprintf("accelerator %s does not support HAMi virtualization", acceleratorType),
		},
	}
}
