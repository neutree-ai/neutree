package main

import (
	"context"
	"fmt"
	"os"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/nodeagent"
	"github.com/neutree-ai/neutree/pkg/nodeagent/adapter"
)

type fixtureAccelerator struct{}

func (fixtureAccelerator) Type() string { return "fixture_accelerator" }

func (fixtureAccelerator) DiscoverHardware(context.Context) (adapter.HardwareSnapshot, error) {
	return adapter.HardwareSnapshot{
		Accelerator: v1.StaticNodeAcceleratorStatus{Type: "fixture_accelerator"},
	}, nil
}

func (fixtureAccelerator) BuildKubernetesMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.KubernetesEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

func (fixtureAccelerator) BuildStaticMetrics(
	context.Context,
	adapter.HardwareSnapshot,
	adapter.StaticEvidence,
) (adapter.MetricResult, error) {
	return adapter.MetricResult{}, nil
}

func main() {
	adapters := append(nodeagent.DefaultAdapters(), fixtureAccelerator{})
	err := nodeagent.Run(context.Background(), nodeagent.Config{
		Args: []string{"version"},
		Build: nodeagent.BuildInfo{
			Version:   "fixture",
			GitCommit: "fixture",
			BuildTime: "fixture",
		},
		Adapters: adapters,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
