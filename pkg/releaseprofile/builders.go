package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Builder constructs the release policy, exact Profiles, and package image
// payload from one immutable Catalog.
type Builder interface {
	CurrentReleaseInfoBaseline() string
	BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error)
	BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error)
	BuildPackageImages(clusterVersion, clusterType, accelerator string) ([]v1.ImageRef, error)
	PackageAccelerators(clusterType string) []string
}

type catalogBuilder struct {
	catalog *Catalog
}

// NewBuilder returns the process-default Builder. Calling it freezes the
// Catalog injection point for the lifetime of the process.
func NewBuilder() Builder {
	return &catalogBuilder{catalog: defaultCatalog()}
}

// NewBuilderForCatalog creates a Builder from an explicit Catalog without
// consuming or changing the process default. It is suitable for generators and
// tests that need a deterministic edition-specific catalog.
func NewBuilderForCatalog(catalog *Catalog) (Builder, error) {
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	if err := validateCatalog(catalog); err != nil {
		return nil, err
	}

	return &catalogBuilder{catalog: cloneCatalog(catalog)}, nil
}

func (builder *catalogBuilder) CurrentReleaseInfoBaseline() string {
	if builder == nil || builder.catalog == nil {
		return ""
	}

	return builder.catalog.spec.CurrentReleaseInfoBaseline
}

func (builder *catalogBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	return builder.catalog.buildReleaseInfo(baseline)
}

func (builder *catalogBuilder) BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	return builder.catalog.buildClusterProfiles(baseline)
}

func (builder *catalogBuilder) BuildPackageImages(clusterVersion, clusterType, accelerator string) ([]v1.ImageRef, error) {
	if builder == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	return builder.catalog.buildPackageImages(clusterVersion, clusterType, accelerator)
}

func (builder *catalogBuilder) PackageAccelerators(clusterType string) []string {
	if builder == nil || builder.catalog == nil {
		return nil
	}

	return builder.catalog.packageAccelerators(clusterType)
}
