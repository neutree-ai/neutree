package staticcluster

import (
	"context"
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/require"
)

func TestPlannerRejectsExternalAcceleratorExporter(t *testing.T) {
	cluster := testStaticNodeCluster()
	cluster.Spec.Metrics = &v1.ClusterMetricsConfig{
		AcceleratorExporter: &v1.ClusterAcceleratorExporterConfig{
			Mode: v1.ClusterAcceleratorExporterModeExternal,
		},
	}

	plans, err := (&Planner{}).Plan(context.Background(), cluster, nil)

	require.Nil(t, plans)
	require.EqualError(t, err, "accelerator_exporter.mode=external is not supported for SSH static clusters")
}
