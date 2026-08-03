package models

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// uniqueViolationCode is the Postgres SQLSTATE for a unique constraint failure.
// PostgREST puts it in the error body and the client hands that body back as the
// error text, so this is how a lost race for an alias arrives.
const uniqueViolationCode = "23505"

// aliasConflictError is the body of a 409. It names the model already holding
// the alias so the caller can say which one it is, rather than only that the
// name is taken.
type aliasConflictError struct {
	Message  string        `json:"message"`
	Conflict aliasConflict `json:"conflict"`
}

// aliasConflict identifies the object holding the alias. Physical name and
// version, never the alias: the point is to tell the user which real model they
// have collided with.
type aliasConflict struct {
	// Kind is "Model" for another model version, or "ModelName" when the alias
	// would shadow a physical model name.
	Kind    string `json:"kind"`
	Name    string `json:"name"`
	Version string `json:"version,omitempty"`
}

const (
	aliasConflictKindModel     = "Model"
	aliasConflictKindModelName = "ModelName"
)

// aliasKey identifies a model version inside one registry. Physical coordinates
// are lowercase on disk, so they are compared lowercase.
func aliasKey(modelName, version string) string {
	return strings.ToLower(modelName) + ":" + strings.ToLower(version)
}

// listRegistryAliases returns every alias row recorded for a registry, keyed by
// the model version it names. Rows whose model no longer exists are still in
// here; callers that render aliases must look them up by a live model, which is
// what keeps an orphan invisible.
func listRegistryAliases(deps *Dependencies, registryID int) (map[string]v1.ModelAlias, error) {
	rows, err := listAliasRows(deps, registryID)
	if err != nil {
		return nil, err
	}

	aliases := make(map[string]v1.ModelAlias, len(rows))
	for _, row := range rows {
		aliases[aliasKey(row.ModelName, row.ModelVersion)] = row
	}

	return aliases, nil
}

// attachAliases fills in the alias of every version in a listing. A registry
// with no aliases costs one query and no further work.
func attachAliases(models []v1.GeneralModel, aliases map[string]v1.ModelAlias) {
	if len(aliases) == 0 {
		return
	}

	for i := range models {
		for j := range models[i].Versions {
			row, ok := aliases[aliasKey(models[i].Name, models[i].Versions[j].Name)]
			if ok {
				models[i].Versions[j].Alias = row.Alias
			}
		}
	}
}

// setModelAlias records alias for one model version, enforcing that it is unique
// within the registry and does not shadow a physical model name.
//
// It returns the HTTP status to answer with and, for a conflict, the body
// describing what the alias collided with.
func setModelAlias(deps *Dependencies, handle *registryHandle,
	modelName, version, alias string) (int, any, error) {
	rows, err := listAliasRows(deps, handle.registry.ID)
	if err != nil {
		return http.StatusInternalServerError, nil, err
	}

	existing := findAliasRow(rows, func(row v1.ModelAlias) bool {
		return aliasKey(row.ModelName, row.ModelVersion) == aliasKey(modelName, version)
	})

	if strings.TrimSpace(alias) == "" {
		return clearModelAlias(deps, existing)
	}

	if err := v1.ValidateModelAlias(alias); err != nil {
		return http.StatusBadRequest, gin.H{"message": err.Error()}, nil
	}

	normalized := v1.NormalizeModelAlias(alias)

	page, err := handle.client.ListModels(model_registry.ListOption{})
	if err != nil {
		return http.StatusInternalServerError, nil, fmt.Errorf("failed to list models: %w", err)
	}

	// An alias that equals a physical model name makes the model selector
	// ambiguous: two different entries would offer the same name.
	for _, model := range page.Models {
		if v1.NormalizeModelAlias(model.Name) == normalized {
			return http.StatusConflict, aliasConflictError{
				Message:  fmt.Sprintf("alias %q is already the name of a model in this registry", alias),
				Conflict: aliasConflict{Kind: aliasConflictKindModelName, Name: model.Name},
			}, nil
		}
	}

	status, body, err := resolveAliasHolder(deps, rows, page, normalized, existing, alias)
	if err != nil || status != 0 {
		return status, body, err
	}

	return writeModelAlias(deps, handle.registry, existing, modelName, version, alias, normalized)
}

// resolveAliasHolder deals with a row that already holds the normalized alias.
// A live model keeps it; a row whose model has since disappeared does not
// reserve anything and is cleared out of the way.
//
// A zero status means "nothing in the way, carry on".
func resolveAliasHolder(deps *Dependencies, rows []v1.ModelAlias, page *model_registry.ModelPage,
	normalized string, existing *v1.ModelAlias, alias string) (int, any, error) {
	holder := findAliasRow(rows, func(row v1.ModelAlias) bool {
		return row.AliasNormalized == normalized
	})

	if holder == nil || (existing != nil && holder.ID == existing.ID) {
		return 0, nil, nil
	}

	if modelVersionExists(page, holder.ModelName, holder.ModelVersion) {
		return http.StatusConflict, aliasConflictError{
			Message: fmt.Sprintf("alias %q is already used in this registry", alias),
			Conflict: aliasConflict{
				Kind:    aliasConflictKindModel,
				Name:    holder.ModelName,
				Version: holder.ModelVersion,
			},
		}, nil
	}

	// The holder is an orphan: the alias table is a projection of the registry
	// filesystem, and the filesystem no longer has that model.
	if err := deps.Storage.DeleteModelAlias(strconv.Itoa(holder.ID)); err != nil {
		return http.StatusInternalServerError, nil, fmt.Errorf("failed to release orphaned alias: %w", err)
	}

	return 0, nil, nil
}

func clearModelAlias(deps *Dependencies, existing *v1.ModelAlias) (int, any, error) {
	if existing == nil {
		return 0, nil, nil
	}

	if err := deps.Storage.DeleteModelAlias(strconv.Itoa(existing.ID)); err != nil {
		return http.StatusInternalServerError, nil, fmt.Errorf("failed to delete model alias: %w", err)
	}

	return 0, nil, nil
}

func writeModelAlias(deps *Dependencies, registry *v1.ModelRegistry, existing *v1.ModelAlias,
	modelName, version, alias, normalized string) (int, any, error) {
	row := &v1.ModelAlias{
		ModelRegistryID: registry.ID,
		ModelName:       strings.ToLower(modelName),
		ModelVersion:    strings.ToLower(version),
		Alias:           alias,
		AliasNormalized: normalized,
	}

	var err error
	if existing != nil {
		err = deps.Storage.UpdateModelAlias(strconv.Itoa(existing.ID), row)
	} else {
		err = deps.Storage.CreateModelAlias(row)
	}

	if err != nil {
		// The checks above ran against a snapshot; the unique index is what
		// actually arbitrates, and losing that race is a conflict, not a failure.
		if isUniqueViolation(err) {
			return http.StatusConflict, aliasConflictError{
				Message:  fmt.Sprintf("alias %q is already used in this registry", alias),
				Conflict: aliasHolderAfterRace(deps, registry.ID, normalized),
			}, nil
		}

		return http.StatusInternalServerError, nil, fmt.Errorf("failed to save model alias: %w", err)
	}

	return 0, nil, nil
}

// aliasHolderAfterRace re-reads who ended up with the alias, so a caller that
// lost the race is told which model it collided with rather than only that it
// collided. A failed re-read still yields a conflict — just a less specific one.
func aliasHolderAfterRace(deps *Dependencies, registryID int, normalized string) aliasConflict {
	conflict := aliasConflict{Kind: aliasConflictKindModel}

	rows, err := listAliasRows(deps, registryID)
	if err != nil {
		klog.Warningf("failed to identify the holder of alias %q: %v", normalized, err)

		return conflict
	}

	holder := findAliasRow(rows, func(row v1.ModelAlias) bool {
		return row.AliasNormalized == normalized
	})
	if holder == nil {
		return conflict
	}

	conflict.Name = holder.ModelName
	conflict.Version = holder.ModelVersion

	return conflict
}

// deleteModelAliases drops the alias rows attached to a model version. It is
// best effort by design: a model that is gone from disk must not stay
// undeletable because its alias row would not go away, and a leftover row is
// already invisible to every read path.
func deleteModelAliases(deps *Dependencies, registryID int, modelName, version string) {
	rows, err := listAliasRows(deps, registryID)
	if err != nil {
		klog.Warningf("failed to look up aliases of deleted model %s:%s: %v", modelName, version, err)

		return
	}

	row := findAliasRow(rows, func(candidate v1.ModelAlias) bool {
		return aliasKey(candidate.ModelName, candidate.ModelVersion) == aliasKey(modelName, version)
	})
	if row == nil {
		return
	}

	if err := deps.Storage.DeleteModelAlias(strconv.Itoa(row.ID)); err != nil {
		klog.Warningf("failed to delete alias of deleted model %s:%s: %v", modelName, version, err)
	}
}

// listAliasRows reads every alias row recorded for one registry. Uniqueness and
// orphan questions are both answered against the same snapshot, so they are
// answered from one read.
func listAliasRows(deps *Dependencies, registryID int) ([]v1.ModelAlias, error) {
	rows, err := deps.Storage.ListModelAlias(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "model_registry_id",
				Operator: "eq",
				Value:    strconv.Itoa(registryID),
			},
		},
	})
	if err != nil {
		return nil, fmt.Errorf("failed to list model aliases: %w", err)
	}

	return rows, nil
}

func findAliasRow(rows []v1.ModelAlias, match func(v1.ModelAlias) bool) *v1.ModelAlias {
	for i := range rows {
		if match(rows[i]) {
			return &rows[i]
		}
	}

	return nil
}

func modelVersionExists(page *model_registry.ModelPage, modelName, version string) bool {
	for _, model := range page.Models {
		if !strings.EqualFold(model.Name, modelName) {
			continue
		}

		for _, candidate := range model.Versions {
			if strings.EqualFold(candidate.Name, version) {
				return true
			}
		}
	}

	return false
}

func isUniqueViolation(err error) bool {
	return err != nil && strings.Contains(err.Error(), uniqueViolationCode)
}
