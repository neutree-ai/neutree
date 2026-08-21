package app_test

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	coreapp "github.com/neutree-ai/neutree/cmd/neutree-core/app"
)

type externalReleaseInfoBuilder struct{}

func (externalReleaseInfoBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

type externalClusterProfileBuilder struct{}

func (externalClusterProfileBuilder) BuildClusterProfile(string, string) (*v1.ClusterProfile, error) {
	return nil, nil
}

func TestBuilderAcceptsExternalReleaseProfileBuilders(t *testing.T) {
	builder := coreapp.NewBuilder().
		WithReleaseInfoBuilder(externalReleaseInfoBuilder{}).
		WithCurrentClusterProfileBuilder(externalClusterProfileBuilder{})

	if builder == nil {
		t.Fatal("expected Core builder")
	}
}
