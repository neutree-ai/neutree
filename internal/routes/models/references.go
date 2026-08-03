package models

import (
	"fmt"
	"sort"
	"strconv"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Kinds a model reference can name. They match the resource kinds the scheme
// registers, so a client can route from a reference back to the object.
const (
	endpointKind     = "Endpoint"
	modelCatalogKind = "ModelCatalog"
)

// ModelReference is one object standing in the way of deleting a model. It
// carries enough for a user to go and change that object: what kind it is, where
// it lives, and — for a recipe catalog — which variant inside it points at the
// model.
type ModelReference struct {
	Kind      string `json:"kind"`
	Name      string `json:"name"`
	Workspace string `json:"workspace"`
	// Phase is the referring endpoint's phase. A deployment that is still coming
	// up (Deploying, ModelDownloading) is a reference just as much as a running
	// one; it is not a separate kind of object, so the phase is what lets a
	// client tell them apart.
	Phase string `json:"phase,omitempty"`
	// Variant is the recipe variant key inside a model catalog. Without it a user
	// facing a catalog with a dozen variants does not know which one to edit.
	Variant string `json:"variant,omitempty"`
}

// collectModelReferences finds everything that still points at a model version.
func collectModelReferences(deps *Dependencies, workspace, registryName, modelName, version string) ([]ModelReference, error) {
	references, err := endpointReferences(deps, workspace, registryName, modelName, version)
	if err != nil {
		return nil, err
	}

	catalogReferences, err := modelCatalogReferences(deps, workspace, registryName, modelName, version)
	if err != nil {
		return nil, err
	}

	references = append(references, catalogReferences...)

	sort.Slice(references, func(i, j int) bool {
		if references[i].Kind != references[j].Kind {
			return references[i].Kind < references[j].Kind
		}

		if references[i].Name != references[j].Name {
			return references[i].Name < references[j].Name
		}

		return references[i].Variant < references[j].Variant
	})

	return references, nil
}

func endpointReferences(deps *Dependencies, workspace, registryName, modelName, version string) ([]ModelReference, error) {
	endpoints, err := deps.Storage.ListEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
			{
				Column:   "spec->model->>registry",
				Operator: "eq",
				Value:    registryName,
			},
			{
				Column:   "spec->model->>name",
				Operator: "eq",
				Value:    modelName,
			},
			{
				// An endpoint marked for deletion is on its way out and must not
				// hold a model hostage: without this the user deletes the endpoint,
				// still cannot delete the model, and has no way to break the
				// deadlock.
				Column:   "metadata->>deletion_timestamp",
				Operator: "is",
				Value:    "null",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list endpoints: %w", err)
	}

	var references []ModelReference

	for i := range endpoints {
		endpoint := endpoints[i]
		if endpoint.Spec == nil || endpoint.Spec.Model == nil {
			continue
		}

		if !versionsOverlap(endpoint.Spec.Model.Version, version) {
			continue
		}

		reference := ModelReference{
			Kind:      endpointKind,
			Name:      endpoint.GetName(),
			Workspace: endpoint.GetWorkspace(),
		}

		if endpoint.Status != nil {
			reference.Phase = string(endpoint.Status.Phase)
		}

		references = append(references, reference)
	}

	return references, nil
}

// modelCatalogReferences scans the workspace's model catalogs in Go rather than
// filtering in the database. A recipe catalog keeps its variants in a JSON object
// with arbitrary keys, so "any variant points at this model" is not expressible
// as a filter on one column, and inventing a SQL function for it would put the
// matching rules in two places.
func modelCatalogReferences(deps *Dependencies, workspace, registryName, modelName, version string) ([]ModelReference, error) {
	catalogs, err := deps.Storage.ListModelCatalog(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
			{
				Column:   "metadata->>deletion_timestamp",
				Operator: "is",
				Value:    "null",
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list model catalogs: %w", err)
	}

	var references []ModelReference

	for i := range catalogs {
		catalog := catalogs[i]
		if catalog.Spec == nil {
			continue
		}

		if modelSpecReferences(catalog.Spec.Model, registryName, modelName, version) {
			references = append(references, ModelReference{
				Kind:      modelCatalogKind,
				Name:      catalog.GetName(),
				Workspace: catalog.GetWorkspace(),
			})
		}

		for variantKey := range catalog.Spec.Variants {
			variant := catalog.Spec.Variants[variantKey]
			if !modelSpecReferences(variant.Model, registryName, modelName, version) {
				continue
			}

			references = append(references, ModelReference{
				Kind:      modelCatalogKind,
				Name:      catalog.GetName(),
				Workspace: catalog.GetWorkspace(),
				Variant:   variantKey,
			})
		}
	}

	return references, nil
}

// modelSpecReferences reports whether a model spec points at the model.
//
// A spec that names no registry matches any of them. Catalog validation does not
// require the field — a recipe variant only has to name a model — so treating an
// empty registry as "no opinion" is what keeps a real reference from slipping
// past the check. It can over-match, which costs the user an explanation; the
// other way round costs them a model deleted out from under a catalog.
func modelSpecReferences(spec *v1.ModelSpec, registryName, modelName, version string) bool {
	if spec == nil || spec.Name != modelName {
		return false
	}

	if spec.Registry != "" && spec.Registry != registryName {
		return false
	}

	return versionsOverlap(spec.Version, version)
}

// versionsOverlap applies the wildcard rule for model versions: "latest" is not
// a version but a pointer to one, so it matches whatever the other side names,
// in either direction. An unset version means "latest".
func versionsOverlap(referencedVersion, version string) bool {
	if referencedVersion == "" {
		referencedVersion = v1.LatestVersion
	}

	if version == "" {
		version = v1.LatestVersion
	}

	return version == v1.LatestVersion || referencedVersion == v1.LatestVersion || referencedVersion == version
}

// referenceHint describes the blockers in the terms the caller will recognise.
// The endpoint-only wording is the long-standing one and is asserted by the
// existing tests, so it is reproduced exactly.
func referenceHint(references []ModelReference) string {
	var endpoints, catalogs int

	for _, reference := range references {
		switch reference.Kind {
		case endpointKind:
			endpoints++
		case modelCatalogKind:
			catalogs++
		}
	}

	switch {
	case catalogs == 0:
		return fmt.Sprintf("%d endpoint(s) still reference this model", endpoints)
	case endpoints == 0:
		return fmt.Sprintf("%d model catalog(s) still reference this model", catalogs)
	default:
		return fmt.Sprintf("%d endpoint(s) and %d model catalog(s) still reference this model", endpoints, catalogs)
	}
}
