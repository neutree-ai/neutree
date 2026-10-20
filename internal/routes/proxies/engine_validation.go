package proxies

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils/request"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type engineValidationOperation string

const (
	engineValidationPatch      engineValidationOperation = "patch"
	engineValidationSoftDelete engineValidationOperation = "soft_delete"
)

type engineValidationInput struct {
	RawPayload  map[string]json.RawMessage
	Patch       v1.Engine
	Current     *v1.Engine
	New         *v1.Engine
	QueryParams url.Values
	Operation   engineValidationOperation
	HasSpec     bool
	HasMetadata bool
}

func validateEngineRequest(store storage.Storage) gin.HandlerFunc {
	return func(context *gin.Context) {
		if context.Request.Method != http.MethodPatch {
			context.Next()
			return
		}

		input, validationErr := readEngineValidationInput(context)
		if validationErr != nil {
			context.AbortWithStatusJSON(validationErrStatus(validationErr), validationErr)
			return
		}

		if validationErr := prepareEngineValidationInput(store, input); validationErr != nil {
			context.Header("X-Powered-By", "Neutree")
			context.AbortWithStatusJSON(validationErrStatus(validationErr), validationErr)

			return
		}

		if validationErr := validateEngineInput(store, input); validationErr != nil {
			context.Header("X-Powered-By", "Neutree")
			context.AbortWithStatusJSON(validationErrStatus(validationErr), validationErr)

			return
		}

		context.Next()
	}
}

func readEngineValidationInput(context *gin.Context) (*engineValidationInput, *validationError) {
	body, readErr := io.ReadAll(context.Request.Body)
	closeErr := context.Request.Body.Close()

	if readErr != nil || closeErr != nil {
		return nil, invalidEnginePatchError("failed to read request body")
	}

	input := &engineValidationInput{
		QueryParams: context.Request.URL.Query(),
		Operation:   engineValidationPatch,
	}
	if err := json.Unmarshal(body, &input.RawPayload); err != nil || input.RawPayload == nil {
		return nil, invalidEnginePatchError("request body must be a JSON object")
	}

	request.RestoreBody(context, body)

	hasSpec, hasMetadata, validationErr := enginePatchFields(input.QueryParams, input.RawPayload)
	if validationErr != nil {
		return nil, validationErr
	}

	patch, err := decodeEnginePatch(input.RawPayload, hasSpec, hasMetadata)
	if err != nil {
		return nil, invalidEnginePatchError("invalid engine metadata or spec")
	}

	input.Patch = *patch
	input.HasSpec = hasSpec
	input.HasMetadata = hasMetadata

	if patch.GetDeletionTimestamp() != "" {
		input.Operation = engineValidationSoftDelete
	}

	return input, nil
}

func decodeEnginePatch(payload map[string]json.RawMessage, hasSpec, hasMetadata bool) (*v1.Engine, error) {
	var patch v1.Engine
	var err error

	if hasSpec {
		patch.Spec, err = decodeEngineReferenceSpec(payload["spec"])
		if err != nil {
			return nil, err
		}
	}

	if hasMetadata {
		patch.Metadata, err = decodeEngineReferenceMetadata(payload["metadata"])
		if err != nil {
			return nil, err
		}
	}

	return &patch, nil
}

func decodeEngineReferenceSpec(raw json.RawMessage) (*v1.EngineSpec, error) {
	fields, err := decodeEngineReferenceObject(raw, "versions")
	if err != nil || fields == nil {
		return nil, err
	}

	var encodedVersions []json.RawMessage
	if value, supplied := fields["versions"]; supplied {
		if err := json.Unmarshal(value, &encodedVersions); err != nil {
			return nil, err
		}
	}

	versions := make([]*v1.EngineVersion, 0, len(encodedVersions))

	for _, encodedVersion := range encodedVersions {
		versionFields, err := decodeEngineReferenceObject(encodedVersion, "version")
		if err != nil {
			return nil, err
		}

		if versionFields == nil {
			versions = append(versions, nil)
			continue
		}

		var version string
		if value, supplied := versionFields["version"]; supplied {
			if err := json.Unmarshal(value, &version); err != nil {
				return nil, err
			}
		}

		versions = append(versions, &v1.EngineVersion{Version: version})
	}

	return &v1.EngineSpec{Versions: versions}, nil
}

func decodeEngineReferenceMetadata(raw json.RawMessage) (*v1.Metadata, error) {
	fields, err := decodeEngineReferenceObject(raw, "name", "workspace", "deletion_timestamp")
	if err != nil || fields == nil {
		return nil, err
	}

	metadata := &v1.Metadata{}
	for column, destination := range map[string]*string{
		"name": &metadata.Name, "workspace": &metadata.Workspace, "deletion_timestamp": &metadata.DeletionTimestamp,
	} {
		if value, supplied := fields[column]; supplied {
			if err := json.Unmarshal(value, destination); err != nil {
				return nil, err
			}
		}
	}

	return metadata, nil
}

func decodeEngineReferenceObject(raw json.RawMessage, canonicalFields ...string) (map[string]json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil
	}

	var fields map[string]json.RawMessage
	if err := json.Unmarshal(raw, &fields); err != nil {
		return nil, err
	}

	for field := range fields {
		for _, canonical := range canonicalFields {
			if field != canonical && strings.EqualFold(field, canonical) {
				return nil, errors.New("engine reference fields must use canonical JSON names")
			}
		}
	}

	return fields, nil
}

func enginePatchFields(query url.Values, payload map[string]json.RawMessage) (bool, bool, *validationError) {
	_, hasSpec := payload["spec"]
	_, hasMetadata := payload["metadata"]

	columns, explicitColumns := query["columns"]
	if !explicitColumns {
		return hasSpec, hasMetadata, nil
	}

	if len(columns) != 1 {
		return false, false, invalidEnginePatchError("engine PATCH columns must not be repeated")
	}

	hasSpec, hasMetadata = false, false

	for _, column := range strings.Split(columns[0], ",") {
		switch strings.Trim(strings.TrimSpace(column), `"`) {
		case "spec":
			hasSpec = true
		case "metadata":
			hasMetadata = true
		case "id", "api_version", "kind", "status":
		default:
			return false, false, invalidEnginePatchError("unsupported engine PATCH column")
		}
	}

	return hasSpec, hasMetadata, nil
}

func prepareEngineValidationInput(store storage.Storage, input *engineValidationInput) *validationError {
	if !input.HasSpec && !input.HasMetadata {
		return nil
	}

	filters, validationErr := enginePatchLookupFilters(input.QueryParams)
	if validationErr != nil {
		return validationErr
	}

	if store == nil {
		return internalServerValidationError()
	}

	engines, err := store.ListEngine(storage.ListOption{Filters: filters})
	if err != nil {
		klog.Errorf("Failed to resolve engine PATCH target: %v", err)
		return internalServerValidationError()
	}

	if len(engines) != 1 {
		return invalidEnginePatchError("PATCH must target exactly one engine")
	}

	current := &engines[0]
	if current.GetName() == "" || current.GetWorkspace() == "" {
		return internalServerValidationError()
	}

	next := *current
	if input.HasSpec {
		next.Spec = input.Patch.Spec
	}

	if input.HasMetadata {
		next.Metadata = input.Patch.Metadata
	}

	input.Current = current
	input.New = &next

	return nil
}

func validateEngineInput(store storage.Storage, input *engineValidationInput) *validationError {
	if !input.HasSpec && !input.HasMetadata {
		return nil
	}

	current, next := input.Current, input.New
	if current == nil || next == nil {
		return internalServerValidationError()
	}

	identityChanged := next.GetName() != current.GetName() || next.GetWorkspace() != current.GetWorkspace()
	if input.Operation == engineValidationSoftDelete || identityChanged || (input.HasSpec && next.Spec == nil) {
		return validateEngineReferences(store, current, "")
	}

	if !input.HasSpec || current.Spec == nil {
		return nil
	}

	retainedVersions := make(map[string]struct{}, len(next.Spec.Versions))

	for _, version := range next.Spec.Versions {
		if version != nil {
			retainedVersions[version.Version] = struct{}{}
		}
	}

	for _, version := range current.Spec.Versions {
		if version == nil {
			continue
		}

		if _, retained := retainedVersions[version.Version]; retained {
			continue
		}

		if validationErr := validateEngineReferences(store, current, version.Version); validationErr != nil {
			return validationErr
		}
	}

	return nil
}

func enginePatchLookupFilters(query url.Values) ([]storage.Filter, *validationError) {
	selectors := make(url.Values, len(query))

	for column, values := range query {
		switch column {
		case "select", "columns", "order":
			continue
		case "limit", "offset":
			return nil, invalidEnginePatchError("paginated engine PATCH is not supported")
		case "and", "or", "not.and", "not.or":
			return nil, invalidEnginePatchError("logical engine PATCH selectors are not supported")
		}

		if len(values) != 1 {
			return nil, invalidEnginePatchError("engine PATCH selectors must not be repeated")
		}

		selectors[column] = values
	}

	filters := queryParamsToFilters(selectors)
	if len(filters) == 0 {
		return nil, invalidEnginePatchError("engine lookup filters are required for engine PATCH")
	}

	return filters, nil
}

func validateEngineReferences(store storage.Storage, engine *v1.Engine, version string) *validationError {
	filters := []storage.Filter{
		{Column: "metadata->>workspace", Operator: "eq", Value: engine.GetWorkspace()},
		{Column: "spec->engine->>engine", Operator: "eq", Value: engine.GetName()},
	}
	if version != "" {
		filters = append(filters, storage.Filter{Column: "spec->engine->>version", Operator: "eq", Value: version})
	}

	count, err := store.Count(storage.ENDPOINT_TABLE, filters)
	if err != nil {
		klog.Errorf("Failed to check engine references: %v", err)
		return internalServerValidationError()
	}

	if count > 0 {
		return &validationError{
			Code:    "10133",
			Message: "cannot remove an engine or version that is still referenced",
			Hint:    "Remove endpoint references before deleting the engine or version",
		}
	}

	return nil
}

func invalidEnginePatchError(hint string) *validationError {
	return &validationError{Code: "400", Message: "invalid engine PATCH", Hint: hint}
}
