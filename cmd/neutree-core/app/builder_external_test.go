package app_test

import (
	"testing"

	v1 "github.com/neutree-ai/neutree/api/v1"
	coreapp "github.com/neutree-ai/neutree/cmd/neutree-core/app"
)

type externalReleaseProfileBuilder struct{}

func (externalReleaseProfileBuilder) CurrentReleaseInfoBaseline() string {
	return "v1.2.0"
}

func (externalReleaseProfileBuilder) BuildReleaseInfo(string) (*v1.ReleaseInfo, error) {
	return nil, nil
}

func (externalReleaseProfileBuilder) BuildClusterProfiles(string) ([]*v1.ClusterProfile, error) {
	return nil, nil
}

func (externalReleaseProfileBuilder) BuildPackageImages(string, string, string) ([]v1.ImageRef, error) {
	return nil, nil
}

func (externalReleaseProfileBuilder) PackageAccelerators(string) []string {
	return nil
}

func TestBuilderAcceptsExternalReleaseProfileBuilder(t *testing.T) {
	builder := coreapp.NewBuilder().WithReleaseProfileBuilder(externalReleaseProfileBuilder{})

	if builder == nil {
		t.Fatal("expected Core builder")
	}
}
