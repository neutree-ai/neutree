package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/internal/middleware"
	"github.com/neutree-ai/neutree/internal/utils/request"
	"github.com/neutree-ai/neutree/pkg/admission"
)

var errPatchAdmissionTargetRead = errors.New("caller-scoped admission target read failed")

type patchAdmissionFailure struct {
	Cause     string
	ErrorType string
}

type legacyDeleteAdmissionResponse interface {
	legacyDeleteAdmissionError() *admission.Error
}

type legacyDeleteDependencyError struct {
	admissionError *admission.Error
}

func newLegacyDeleteDependencyError(code int, message, hint string) error {
	return &legacyDeleteDependencyError{admissionError: &admission.Error{Code: code, Message: message, Hint: hint}}
}

func (e *legacyDeleteDependencyError) Error() string {
	return e.admissionError.Error()
}

func (e *legacyDeleteDependencyError) Unwrap() error {
	return e.admissionError
}

func (e *legacyDeleteDependencyError) legacyDeleteAdmissionError() *admission.Error {
	return e.admissionError
}

// recordPatchAdmissionFailure contains only a classified cause and the Go
// error type. It deliberately never receives error text, which may contain
// request data, credentials, or an upstream URL.
var recordPatchAdmissionFailure = func(failure patchAdmissionFailure) {
	klog.ErrorS(
		errors.New("patch admission failure"),
		"patch admission runner failed",
		"cause", failure.Cause,
		"error_type", failure.ErrorType,
	)
}

type patchAdmissionChain interface {
	Run(admission.RequestContext, any, any) (any, error)
}

type patchAdmissionChainResolver interface {
	Chain(any, admission.Operation) (patchAdmissionChain, error)
}

type patchAdmissionTargetReader interface {
	Read(context.Context, string, url.Values, string) ([]json.RawMessage, error)
}

// PatchAdmissionRunnerOptions permits a resource to preserve a pre-existing
// client error contract for malformed PATCH payloads. The callback receives
// the original request body so resource-owned parsers can retain their legacy
// message and hint without exposing the body to admission hooks.
type PatchAdmissionRunnerOptions struct {
	InvalidRequestError    func([]byte, error) *admission.Error
	InvalidRequestResponse func([]byte, error) error
	BodyError              func(error) *admission.Error
	BodyResponse           func(error) error
	PermissiveCandidates   bool
	DropEmptyMaskedFields  []string
}

type registryPatchAdmissionChainResolver struct {
	registry *admission.Registry
}

func (r registryPatchAdmissionChainResolver) Chain(resource any, operation admission.Operation) (patchAdmissionChain, error) {
	if r.registry == nil {
		return nil, errors.New("admission registry is unavailable")
	}

	return r.registry.Chain(resource, operation)
}

type postgrestPatchAdmissionTargetReader struct {
	baseURL string
	client  *http.Client
}

func (r postgrestPatchAdmissionTargetReader) Read(ctx context.Context, table string, selectors url.Values, token string) ([]json.RawMessage, error) {
	if token == "" {
		return nil, errPatchAdmissionTargetRead
	}

	target, err := url.Parse(strings.TrimRight(r.baseURL, "/") + "/" + url.PathEscape(table))
	if err != nil {
		return nil, errPatchAdmissionTargetRead
	}

	targetQuery := make(url.Values, len(selectors)+1)
	for key, values := range selectors {
		targetQuery[key] = append([]string(nil), values...)
	}

	targetQuery.Set("select", "*")
	target.RawQuery = targetQuery.Encode()

	httpRequest, err := http.NewRequestWithContext(ctx, http.MethodGet, target.String(), nil)
	if err != nil {
		return nil, errPatchAdmissionTargetRead
	}

	httpRequest.Header.Set("Authorization", "Bearer "+token)

	client := r.client
	if client == nil {
		client = http.DefaultClient
	}

	response, err := client.Do(httpRequest)
	if err != nil {
		return nil, errPatchAdmissionTargetRead
	}

	defer response.Body.Close()

	if response.StatusCode < http.StatusOK || response.StatusCode >= http.StatusMultipleChoices {
		return nil, errPatchAdmissionTargetRead
	}

	var resources []json.RawMessage

	decoder := json.NewDecoder(response.Body)
	if err := decoder.Decode(&resources); err != nil {
		return nil, errPatchAdmissionTargetRead
	}

	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return nil, errPatchAdmissionTargetRead
	}

	return resources, nil
}

// CreatePatchAdmissionRunner admits a caller-visible PATCH target before the
// resource proxy persists it. Routes opt into this runner alongside their
// existing struct proxy handler.
func CreatePatchAdmissionRunner[T any](deps *Dependencies, tableName string, resource admission.Resource[T]) gin.HandlerFunc {
	return CreatePatchAdmissionRunnerWithOptions(deps, tableName, resource, PatchAdmissionRunnerOptions{})
}

// CreatePatchAdmissionRunnerWithOptions creates a PATCH admission runner with
// an optional resource-owned mapping for malformed request payloads.
func CreatePatchAdmissionRunnerWithOptions[T any](
	deps *Dependencies,
	tableName string,
	resource admission.Resource[T],
	options PatchAdmissionRunnerOptions,
) gin.HandlerFunc {
	baseURL := ""
	var registry *admission.Registry

	if deps != nil {
		baseURL = deps.StorageAccessURL
		registry = deps.Admission
	}

	return newPatchAdmissionRunnerWithOptions(
		registryPatchAdmissionChainResolver{registry: registry},
		postgrestPatchAdmissionTargetReader{baseURL: baseURL},
		resource,
		tableName,
		options,
	)
}

func newPatchAdmissionRunner[T any](
	resolver patchAdmissionChainResolver, reader patchAdmissionTargetReader, resource admission.Resource[T], tableName string,
) gin.HandlerFunc {
	return newPatchAdmissionRunnerWithOptions(resolver, reader, resource, tableName, PatchAdmissionRunnerOptions{})
}

func newPatchAdmissionRunnerWithOptions[T any](
	resolver patchAdmissionChainResolver, reader patchAdmissionTargetReader, resource admission.Resource[T], tableName string, options PatchAdmissionRunnerOptions,
) gin.HandlerFunc {
	tagConfig := extractStructTagConfig(reflect.TypeFor[T]())
	topLevelFields := extractTopLevelJSONFields(reflect.TypeFor[T]())

	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPatch {
			return
		}

		if err := c.Request.Context().Err(); err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		selectors, err := patchAdmissionSelectors(c.Request.URL.Query())
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
		}

		postgrestToken, ok := callerPostgrestToken(c)
		if !ok {
			writePatchAdmissionError(c, errPatchAdmissionTargetRead)
			return
		}

		originalBody, err := io.ReadAll(c.Request.Body)
		if err != nil {
			writePatchAdmissionBodyError(c, err, options)
			return
		}

		if err := c.Request.Body.Close(); err != nil {
			writePatchAdmissionBodyError(c, err, options)
			return
		}

		normalizedBody, patch, err := normalizeAdmissionPatch(originalBody, topLevelFields)
		if err != nil {
			writePatchAdmissionInvalidRequestError(c, originalBody, err, options)
			return
		}

		patch, droppedEmptyMaskedFields := dropEmptyMaskedAdmissionFields(patch, options.DropEmptyMaskedFields)
		if droppedEmptyMaskedFields {
			normalizedBody, err = json.Marshal(patch)
			if err != nil {
				writePatchAdmissionError(c, err)
				return
			}
		}

		if containsMaskedAdmissionField(patch, tagConfig.excludeFields) {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
		}

		deleteIntent := request.IsSoftDeleteRequest(patch)

		resources, err := reader.Read(c.Request.Context(), tableName, selectors, postgrestToken)
		if err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		switch len(resources) {
		case 0:
			c.AbortWithStatus(http.StatusNotFound)
			return
		case 1:
		default:
			c.AbortWithStatus(http.StatusConflict)
			return
		}

		oldMap, err := normalizeAdmissionOldSnapshot[T](resources[0])
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
		}

		oldMap = filterAdmissionObject(oldMap, tagConfig.excludeFields)
		patch = filterAdmissionObject(patch, tagConfig.excludeFields)
		candidateMap := cloneAdmissionObject(oldMap)
		applyAdmissionPatch(candidateMap, patch)

		oldJSON, err := json.Marshal(oldMap)
		if err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		old, err := decodePatchAdmissionCandidate[T](oldJSON, options)
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
		}

		candidateJSON, err := json.Marshal(candidateMap)
		if err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		candidate, err := decodePatchAdmissionCandidate[T](candidateJSON, options)
		if err != nil {
			writePatchAdmissionInvalidRequestError(c, originalBody, err, options)
			return
		}

		operation := admission.Update

		if deleteIntent {
			if !validSoftDeleteCandidate(oldMap, candidateMap) {
				writePatchAdmissionError(c, errInvalidAdmissionRequest)
				return
			}

			operation = admission.Delete
		}

		chain, err := resolver.Chain(resource, operation)
		if err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		if _, err := chain.Run(admission.RequestContext{Context: c.Request.Context()}, old, candidate); err != nil {
			writePatchAdmissionError(c, err)
			return
		}

		replaceRequestBody(c.Request, normalizedBody)
	}
}

func decodePatchAdmissionCandidate[T any](raw []byte, options PatchAdmissionRunnerOptions) (any, error) {
	if options.PermissiveCandidates {
		return decodePermissiveAdmissionCandidate[T](raw)
	}

	return decodeAdmissionCandidate[T](raw)
}

func callerPostgrestToken(c *gin.Context) (string, bool) {
	if postgrestToken, ok := middleware.GetPostgrestToken(c); ok && postgrestToken != "" {
		return postgrestToken, true
	}

	parts := strings.Fields(c.GetHeader("Authorization"))
	if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") || parts[1] == "" {
		return "", false
	}

	return parts[1], true
}

func patchAdmissionSelectors(query url.Values) (url.Values, error) {
	selectors := make(url.Values)

	for key, values := range query {
		if key == "select" || key == "returning" {
			continue
		}

		if len(values) != 1 {
			return nil, errInvalidAdmissionRequest
		}

		value := values[0]

		switch key {
		case "id", "metadata->>name":
			if !isEqualPatchAdmissionSelector(value) {
				return nil, errInvalidAdmissionRequest
			}
		case "metadata->>workspace":
			if !isEqualPatchAdmissionSelector(value) && value != "is.null" {
				return nil, errInvalidAdmissionRequest
			}
		default:
			return nil, errInvalidAdmissionRequest
		}

		selectors[key] = []string{values[0]}
	}

	if values, ok := selectors["id"]; ok && len(selectors) == 1 && len(values) == 1 {
		return selectors, nil
	}

	if len(selectors) == 2 &&
		isEqualPatchAdmissionSelector(selectors.Get("metadata->>name")) &&
		(isEqualPatchAdmissionSelector(selectors.Get("metadata->>workspace")) || selectors.Get("metadata->>workspace") == "is.null") {
		return selectors, nil
	}

	return nil, errInvalidAdmissionRequest
}

func isEqualPatchAdmissionSelector(value string) bool {
	return strings.HasPrefix(value, "eq.") && len(strings.TrimPrefix(value, "eq.")) > 0
}

func normalizeAdmissionPatch(body []byte, topLevelFields []string) ([]byte, map[string]interface{}, error) {
	normalizedBody, err := filterPayloadToTopLevelFields(body, topLevelFields)
	if err != nil {
		return nil, nil, errInvalidAdmissionRequest
	}

	var patch map[string]interface{}
	if err := json.Unmarshal(normalizedBody, &patch); err != nil || patch == nil {
		return nil, nil, errInvalidAdmissionRequest
	}

	return normalizedBody, patch, nil
}

func decodeAdmissionObject(raw []byte) (map[string]interface{}, error) {
	var object map[string]interface{}
	if err := json.Unmarshal(raw, &object); err != nil || object == nil {
		return nil, errInvalidAdmissionRequest
	}

	return object, nil
}

// normalizeAdmissionOldSnapshot removes additive persisted fields at every
// level by round-tripping through the resource's admitted Go schema. The
// outgoing PATCH remains the independently normalized client payload.
func normalizeAdmissionOldSnapshot[T any](raw []byte) (map[string]interface{}, error) {
	var snapshot T
	if err := json.Unmarshal(raw, &snapshot); err != nil {
		return nil, errInvalidAdmissionRequest
	}

	normalized, err := json.Marshal(snapshot)
	if err != nil {
		return nil, err
	}

	return decodeAdmissionObject(normalized)
}

func cloneAdmissionObject(object map[string]interface{}) map[string]interface{} {
	encoded, _ := json.Marshal(object)
	var clone map[string]interface{}
	_ = json.Unmarshal(encoded, &clone)

	return clone
}

// applyAdmissionPatch mirrors PostgREST's top-level column replacement. JSON
// columns such as metadata and spec are replaced as complete values, rather
// than recursively merged.
func applyAdmissionPatch(target, patch map[string]interface{}) {
	for key, patchValue := range patch {
		target[key] = patchValue
	}
}

func filterAdmissionObject(object map[string]interface{}, excludedFields map[string]struct{}) map[string]interface{} {
	filtered, ok := filterJSONFields(object, excludedFields).(map[string]interface{})
	if !ok {
		return nil
	}

	return filtered
}

// containsMaskedAdmissionField prevents a request from persisting a field
// that the generic typed admission candidate must redact. Resource-specific
// adapters can add an explicit, non-secret representation if they need to
// admit writes to a masked field in the future.
func containsMaskedAdmissionField(patch map[string]interface{}, excludedFields map[string]struct{}) bool {
	for path := range excludedFields {
		if admissionPatchPathExists(patch, strings.Split(path, ".")) {
			return true
		}
	}

	return false
}

// dropEmptyMaskedAdmissionFields removes only an explicit empty object at a
// resource-approved masked path. This preserves legacy clients that serialize
// zero-value nested structs while keeping every non-empty masked write denied.
func dropEmptyMaskedAdmissionFields(patch map[string]interface{}, paths []string) (map[string]interface{}, bool) {
	if len(paths) == 0 {
		return patch, false
	}

	filtered := cloneAdmissionObject(patch)
	changed := false

	for _, path := range paths {
		if dropEmptyAdmissionObjectField(filtered, strings.Split(path, ".")) {
			changed = true
		}
	}

	if !changed {
		return patch, false
	}

	return filtered, true
}

func dropEmptyAdmissionObjectField(object map[string]interface{}, path []string) bool {
	if len(path) == 0 {
		return false
	}

	value, ok := object[path[0]]
	if !ok {
		return false
	}

	if len(path) == 1 {
		emptyObject, ok := value.(map[string]interface{})
		if !ok || len(emptyObject) != 0 {
			return false
		}

		delete(object, path[0])

		return true
	}

	nested, ok := value.(map[string]interface{})
	if !ok {
		return false
	}

	return dropEmptyAdmissionObjectField(nested, path[1:])
}

func admissionPatchPathExists(value interface{}, path []string) bool {
	if len(path) == 0 {
		return true
	}

	switch current := value.(type) {
	case map[string]interface{}:
		next, ok := current[path[0]]
		return ok && admissionPatchPathExists(next, path[1:])
	case []interface{}:
		for _, item := range current {
			if admissionPatchPathExists(item, path) {
				return true
			}
		}
	}

	return false
}

func validSoftDeleteCandidate(old, candidate map[string]interface{}) bool {
	withoutDeleteFields := func(object map[string]interface{}) map[string]interface{} {
		copy := cloneAdmissionObject(object)
		metadata, ok := copy["metadata"].(map[string]interface{})

		if !ok {
			return copy
		}

		delete(metadata, "deletion_timestamp")

		annotations, ok := metadata["annotations"].(map[string]interface{})
		if ok {
			delete(annotations, "neutree.ai/force-delete")

			if len(annotations) == 0 {
				delete(metadata, "annotations")
			}
		}

		return copy
	}

	return reflect.DeepEqual(withoutDeleteFields(old), withoutDeleteFields(candidate))
}

func writePatchAdmissionError(c *gin.Context, err error) {
	var legacyDeleteResponse legacyDeleteAdmissionResponse
	if errors.As(err, &legacyDeleteResponse) {
		admissionError := legacyDeleteResponse.legacyDeleteAdmissionError()

		c.Header("X-Powered-By", "Neutree")
		c.AbortWithStatusJSON(http.StatusBadRequest, gin.H{
			"code":    strconv.Itoa(admissionError.Code),
			"message": admissionError.Message,
			"hint":    admissionError.Hint,
		})

		return
	}

	if writeLegacyAdmissionResponse(c, err) {
		return
	}

	var admissionError *admission.Error
	if errors.As(err, &admissionError) {
		c.AbortWithStatusJSON(http.StatusBadRequest, admissionError)
		return
	}

	if errors.Is(err, errInvalidAdmissionRequest) {
		c.AbortWithStatusJSON(http.StatusBadRequest, admission.Error{
			Code:    admissionInvalidRequestErrorCode,
			Message: errInvalidAdmissionRequest.Error(),
		})

		return
	}

	recordPatchAdmissionFailure(classifyPatchAdmissionFailure(err))
	c.AbortWithStatusJSON(http.StatusInternalServerError, admission.Error{
		Code:    admissionInternalErrorCode,
		Message: "internal admission error",
	})
}

func writePatchAdmissionInvalidRequestError(c *gin.Context, body []byte, cause error, options PatchAdmissionRunnerOptions) {
	if options.InvalidRequestResponse != nil {
		writePatchAdmissionError(c, options.InvalidRequestResponse(body, cause))
		return
	}

	if options.InvalidRequestError != nil {
		if admissionErr := options.InvalidRequestError(body, cause); admissionErr != nil {
			writePatchAdmissionError(c, admissionErr)
			return
		}
	}

	writePatchAdmissionError(c, errInvalidAdmissionRequest)
}

func writePatchAdmissionBodyError(c *gin.Context, cause error, options PatchAdmissionRunnerOptions) {
	if options.BodyResponse != nil {
		writePatchAdmissionError(c, options.BodyResponse(cause))
		return
	}

	if options.BodyError != nil {
		writePatchAdmissionError(c, options.BodyError(cause))
		return
	}

	writePatchAdmissionError(c, cause)
}

func classifyPatchAdmissionFailure(err error) patchAdmissionFailure {
	failure := patchAdmissionFailure{Cause: "unexpected"}

	switch {
	case errors.Is(err, errPatchAdmissionTargetRead):
		failure.Cause = "caller_scoped_target_read"
	case errors.Is(err, context.Canceled):
		failure.Cause = "request_canceled"
	case errors.Is(err, context.DeadlineExceeded):
		failure.Cause = "request_deadline_exceeded"
	}

	if err != nil {
		failure.ErrorType = reflect.TypeOf(err).String()
	}

	return failure
}
