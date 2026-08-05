package proxies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/gin-gonic/gin"
	"k8s.io/klog/v2"

	"github.com/neutree-ai/neutree/pkg/admission"
)

const (
	admissionInternalErrorCode       = 10300
	admissionInvalidRequestErrorCode = 10301
)

var errInvalidAdmissionRequest = errors.New("invalid admission request")

type invalidAdmissionRequestError struct {
	cause error
}

func (e *invalidAdmissionRequestError) Error() string {
	return errInvalidAdmissionRequest.Error()
}

func (e *invalidAdmissionRequestError) Unwrap() error {
	return errInvalidAdmissionRequest
}

// CreateAdmissionRunnerOptions permits a resource to preserve a pre-existing
// client error contract for request decoding. Hooks still run only on decoded
// candidates, and resources without an option retain framework errors.
type CreateAdmissionRunnerOptions struct {
	InvalidRequestError  func([]byte, error) *admission.Error
	ReadBodyError        func(error) *admission.Error
	AllowEmptyBody       bool
	RejectArray          bool
	PermissiveCandidates bool
}

type createAdmissionChain interface {
	Run(admission.RequestContext, any, any) (any, error)
}

type createAdmissionChainResolver interface {
	CreateChain(any) (createAdmissionChain, error)
}

type registryCreateAdmissionChainResolver struct {
	registry *admission.Registry
}

func (r registryCreateAdmissionChainResolver) CreateChain(resource any) (createAdmissionChain, error) {
	if r.registry == nil {
		return nil, errors.New("admission registry is unavailable")
	}
	return r.registry.Chain(resource, admission.Create)
}

// CreateAdmissionRunner returns middleware that admits POST request objects
// against the resource's Create chain before the proxy receives the body.
func CreateAdmissionRunner[T any](registry *admission.Registry, resource admission.Resource[T]) gin.HandlerFunc {
	return CreateAdmissionRunnerWithOptions(registry, resource, CreateAdmissionRunnerOptions{})
}

func newCreateAdmissionRunner[T any](resolver createAdmissionChainResolver, resource admission.Resource[T]) gin.HandlerFunc {
	return newCreateAdmissionRunnerWithOptions(resolver, resource, CreateAdmissionRunnerOptions{})
}

// CreateAdmissionRunnerWithOptions creates a POST admission runner with an
// optional resource-owned mapping for legacy body decoding errors.
func CreateAdmissionRunnerWithOptions[T any](registry *admission.Registry, resource admission.Resource[T], options CreateAdmissionRunnerOptions) gin.HandlerFunc {
	return newCreateAdmissionRunnerWithOptions(registryCreateAdmissionChainResolver{registry: registry}, resource, options)
}

func newCreateAdmissionRunnerWithOptions[T any](resolver createAdmissionChainResolver, resource admission.Resource[T], options CreateAdmissionRunnerOptions) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			return
		}
		if err := c.Request.Context().Err(); err != nil {
			writeCreateAdmissionRunnerError(c, err, nil, options)
			return
		}

		chain, err := resolver.CreateChain(resource)
		if err != nil {
			writeCreateAdmissionRunnerError(c, err, nil, options)
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			if options.ReadBodyError != nil {
				writeAdmissionRunnerError(c, options.ReadBodyError(err))
			} else {
				writeCreateAdmissionRunnerError(c, err, nil, options)
			}
			return
		}
		if err := c.Request.Body.Close(); err != nil {
			writeCreateAdmissionRunnerError(c, err, nil, options)
			return
		}
		if len(bytes.TrimSpace(body)) == 0 && options.AllowEmptyBody {
			replaceRequestBody(c.Request, body)
			return
		}

		approved, err := admitCreateBodyWithOptions[T](c.Request.Context(), chain, body, options)
		if err != nil {
			writeCreateAdmissionRunnerError(c, err, body, options)
			return
		}
		replaceRequestBody(c.Request, approved)
	}
}

func admitCreateBody[T any](ctx context.Context, chain createAdmissionChain, body []byte) ([]byte, error) {
	return admitCreateBodyWithOptions[T](ctx, chain, body, CreateAdmissionRunnerOptions{})
}

func admitCreateBodyWithOptions[T any](ctx context.Context, chain createAdmissionChain, body []byte, options CreateAdmissionRunnerOptions) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errInvalidAdmissionRequest
	}
	switch trimmed[0] {
	case '{':
		approved, err := runCreateAdmissionCandidateWithOptions[T](ctx, chain, trimmed, options)
		if err != nil {
			return nil, err
		}
		return json.Marshal(approved)
	case '[':
		if options.RejectArray {
			return nil, errInvalidAdmissionRequest
		}
		var rawCandidates []json.RawMessage
		if err := decodeSingleJSONValue(trimmed, &rawCandidates); err != nil {
			return nil, err
		}
		approved := make([]any, 0, len(rawCandidates))
		for _, raw := range rawCandidates {
			result, err := runCreateAdmissionCandidateWithOptions[T](ctx, chain, raw, options)
			if err != nil {
				return nil, err
			}
			approved = append(approved, result)
		}
		return json.Marshal(approved)
	default:
		return nil, errInvalidAdmissionRequest
	}
}

func runCreateAdmissionCandidate[T any](ctx context.Context, chain createAdmissionChain, raw []byte) (any, error) {
	return runCreateAdmissionCandidateWithOptions[T](ctx, chain, raw, CreateAdmissionRunnerOptions{})
}

func runCreateAdmissionCandidateWithOptions[T any](ctx context.Context, chain createAdmissionChain, raw []byte, options CreateAdmissionRunnerOptions) (any, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	var candidate any
	var err error
	if options.PermissiveCandidates {
		candidate, err = decodePermissiveAdmissionCandidate[T](raw)
	} else {
		candidate, err = decodeAdmissionCandidate[T](raw)
	}
	if err != nil {
		return nil, err
	}
	return chain.Run(admission.RequestContext{Context: ctx}, nil, candidate)
}

func decodePermissiveAdmissionCandidate[T any](raw []byte) (map[string]any, error) {
	var typed T
	if err := decodeAdmissionJSONValue(json.NewDecoder(bytes.NewReader(raw)), &typed); err != nil {
		return nil, err
	}
	var candidate map[string]any
	if err := decodeAdmissionJSONValue(json.NewDecoder(bytes.NewReader(raw)), &candidate); err != nil {
		return nil, err
	}
	return candidate, nil
}

func decodeAdmissionCandidate[T any](raw []byte) (T, error) {
	var candidate T
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || trimmed[0] != '{' {
		return candidate, errInvalidAdmissionRequest
	}
	decoder := json.NewDecoder(bytes.NewReader(trimmed))
	decoder.DisallowUnknownFields()
	if err := decodeAdmissionJSONValue(decoder, &candidate); err != nil {
		return candidate, err
	}
	return candidate, nil
}

func decodeSingleJSONValue(raw []byte, value any) error {
	return decodeAdmissionJSONValue(json.NewDecoder(bytes.NewReader(raw)), value)
}

func decodeAdmissionJSONValue(decoder *json.Decoder, value any) error {
	if err := decoder.Decode(value); err != nil {
		return &invalidAdmissionRequestError{cause: err}
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return &invalidAdmissionRequestError{cause: err}
	}
	return nil
}

func replaceRequestBody(request *http.Request, body []byte) {
	request.Body = io.NopCloser(bytes.NewReader(body))
	request.ContentLength = int64(len(body))
	request.Header.Set("Content-Length", fmt.Sprintf("%d", len(body)))
}

func writeAdmissionRunnerError(c *gin.Context, err error) {
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

	klog.ErrorS(err, "admission runner failed")
	c.AbortWithStatusJSON(http.StatusInternalServerError, admission.Error{
		Code:    admissionInternalErrorCode,
		Message: "internal admission error",
	})
}

func writeCreateAdmissionRunnerError(c *gin.Context, err error, body []byte, options CreateAdmissionRunnerOptions) {
	if errors.Is(err, errInvalidAdmissionRequest) && options.InvalidRequestError != nil {
		var invalidRequestErr *invalidAdmissionRequestError
		if errors.As(err, &invalidRequestErr) {
			writeAdmissionRunnerError(c, options.InvalidRequestError(body, invalidRequestErr.cause))
			return
		}
		writeAdmissionRunnerError(c, options.InvalidRequestError(body, nil))
		return
	}
	writeAdmissionRunnerError(c, err)
}
