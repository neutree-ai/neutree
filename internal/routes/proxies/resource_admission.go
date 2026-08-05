package proxies

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
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
	baseURL := ""
	var registry *admission.Registry
	if deps != nil {
		baseURL = deps.StorageAccessURL
		registry = deps.Admission
	}
	return newPatchAdmissionRunner(
		registryPatchAdmissionChainResolver{registry: registry},
		postgrestPatchAdmissionTargetReader{baseURL: baseURL},
		resource,
		tableName,
	)
}

func newPatchAdmissionRunner[T any](
	resolver patchAdmissionChainResolver, reader patchAdmissionTargetReader, resource admission.Resource[T], tableName string,
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
			writePatchAdmissionError(c, err)
			return
		}
		if err := c.Request.Body.Close(); err != nil {
			writePatchAdmissionError(c, err)
			return
		}
		normalizedBody, patch, err := normalizeAdmissionPatch(originalBody, topLevelFields)
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
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
		old, err := decodeAdmissionCandidate[T](oldJSON)
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
			return
		}
		candidateJSON, err := json.Marshal(candidateMap)
		if err != nil {
			writePatchAdmissionError(c, err)
			return
		}
		candidate, err := decodeAdmissionCandidate[T](candidateJSON)
		if err != nil {
			writePatchAdmissionError(c, errInvalidAdmissionRequest)
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
		if len(values) != 1 || !strings.HasPrefix(values[0], "eq.") || len(strings.TrimPrefix(values[0], "eq.")) == 0 {
			return nil, errInvalidAdmissionRequest
		}
		selectors[key] = []string{values[0]}
	}

	if values, ok := selectors["id"]; ok && len(selectors) == 1 && len(values) == 1 {
		return selectors, nil
	}
	if len(selectors) == 2 && selectors.Get("metadata->>name") != "" && selectors.Get("metadata->>workspace") != "" {
		return selectors, nil
	}
	return nil, errInvalidAdmissionRequest
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
