package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Builder constructs the current ReleaseInfo and exact ClusterProfiles from
// one immutable Catalog.
type Builder interface {
	CurrentReleaseInfoBaseline() string
	BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error)
	BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error)
}

// PackageArtifactBuilder adds package-rendering operations without widening
// the runtime Builder contract used by Core and CLI preflight.
type PackageArtifactBuilder interface {
	Builder
	BuildPackageImages(clusterVersion, clusterType, accelerator string) ([]v1.ImageRef, error)
	PackageAccelerators(clusterType string) []string
}

type catalogBuilder struct {
	catalog *Catalog
}

// NewBuilder creates a Builder from the process catalog.
func NewBuilder() Builder {
	return &catalogBuilder{catalog: defaultCatalog()}
}

// NewBuilderForCatalog creates a deterministic package artifact Builder
// without reading or altering the process catalog. Tests and catalog
// generators use this form.
func NewBuilderForCatalog(catalog *Catalog) (PackageArtifactBuilder, error) {
	if catalog == nil {
		return nil, fmt.Errorf("release profile catalog is required")
	}

	return &catalogBuilder{catalog: cloneCatalog(catalog)}, nil
}

// NewPackageArtifactBuilder returns the process-default package artifact
// builder. It observes the same Catalog injection boundary as NewBuilder.
func NewPackageArtifactBuilder() PackageArtifactBuilder {
	return &catalogBuilder{catalog: defaultCatalog()}
}

func (builder *catalogBuilder) CurrentReleaseInfoBaseline() string {
	if builder == nil || builder.catalog == nil {
		return ""
	}

	return builder.catalog.spec.CurrentReleaseInfoBaseline
}

func (builder *catalogBuilder) BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error) {
	if builder == nil || builder.catalog == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	return builder.catalog.buildReleaseInfo(baseline)
}

func (builder *catalogBuilder) BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error) {
	if builder == nil || builder.catalog == nil {
		return nil, fmt.Errorf("release profile builder is required")
	}

	return builder.catalog.buildClusterProfiles(baseline)
}

func (builder *catalogBuilder) BuildPackageImages(clusterVersion, clusterType, accelerator string) ([]v1.ImageRef, error) {
	if builder == nil || builder.catalog == nil {
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
