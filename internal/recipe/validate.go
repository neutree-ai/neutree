package recipe

import (
	"fmt"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// ValidateModelCatalogSpec enforces recipe-MC invariants that can be checked
// statically. Trivial MCs (no variants) always pass.
//
//   - Reject mixing top-level model/resources with variants — that combo is
//     ambiguous because compose's normalize step would have to choose which
//     one wins.
//   - Every feature's conflicts_with entry must reference a real feature key
//     so misconfigured catalogs fail at import time, not at compose time.
func ValidateModelCatalogSpec(spec *v1.ModelCatalogSpec) error {
	if spec == nil {
		return nil
	}

	if len(spec.Variants) > 0 {
		if spec.Model != nil {
			return fmt.Errorf("model_catalog: cannot set top-level model together with variants")
		}

		if spec.Resources != nil {
			return fmt.Errorf("model_catalog: cannot set top-level resources together with variants")
		}
	}

	for name, feat := range spec.Features {
		for _, other := range feat.ConflictsWith {
			if other == name {
				return fmt.Errorf("model_catalog: feature %q lists itself in conflicts_with", name)
			}

			if _, ok := spec.Features[other]; !ok {
				return fmt.Errorf("model_catalog: feature %q conflicts_with unknown feature %q", name, other)
			}
		}
	}

	return nil
}
