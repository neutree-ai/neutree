package releaseprofile

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// Builder constructs the current ReleaseInfo and its exact ClusterProfiles
// from one immutable Catalog.
type Builder interface {
	CurrentReleaseInfoBaseline() string
	BuildReleaseInfo(baseline string) (*v1.ReleaseInfo, error)
	BuildClusterProfiles(baseline string) ([]*v1.ClusterProfile, error)
}

type catalogBuilder struct {
	catalog *Catalog
}

// NewBuilder creates a Builder from the process default. Its first call closes
// the InjectCatalog window for this process.
func NewBuilder() Builder {
	return &catalogBuilder{catalog: defaultCatalog()}
}

// NewBuilderForCatalog creates a deterministic Builder without consuming or
// altering the process default. Tests and catalog generators use this form.
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
