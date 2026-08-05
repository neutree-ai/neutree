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
	return newCreateAdmissionRunner(registryCreateAdmissionChainResolver{registry: registry}, resource)
}

func newCreateAdmissionRunner[T any](resolver createAdmissionChainResolver, resource admission.Resource[T]) gin.HandlerFunc {
	return func(c *gin.Context) {
		if c.Request.Method != http.MethodPost {
			return
		}
		if err := c.Request.Context().Err(); err != nil {
			writeAdmissionRunnerError(c, err)
			return
		}

		chain, err := resolver.CreateChain(resource)
		if err != nil {
			writeAdmissionRunnerError(c, err)
			return
		}

		body, err := io.ReadAll(c.Request.Body)
		if err != nil {
			writeAdmissionRunnerError(c, err)
			return
		}
		if err := c.Request.Body.Close(); err != nil {
			writeAdmissionRunnerError(c, err)
			return
		}

		approved, err := admitCreateBody[T](c.Request.Context(), chain, body)
		if err != nil {
			writeAdmissionRunnerError(c, err)
			return
		}
		replaceRequestBody(c.Request, approved)
	}
}

func admitCreateBody[T any](ctx context.Context, chain createAdmissionChain, body []byte) ([]byte, error) {
	trimmed := bytes.TrimSpace(body)
	if len(trimmed) == 0 {
		return nil, errInvalidAdmissionRequest
	}
	switch trimmed[0] {
	case '{':
		approved, err := runCreateAdmissionCandidate[T](ctx, chain, trimmed)
		if err != nil {
			return nil, err
		}
		return json.Marshal(approved)
	case '[':
		var rawCandidates []json.RawMessage
		if err := decodeSingleJSONValue(trimmed, &rawCandidates); err != nil {
			return nil, err
		}
		approved := make([]any, 0, len(rawCandidates))
		for _, raw := range rawCandidates {
			result, err := runCreateAdmissionCandidate[T](ctx, chain, raw)
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
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	candidate, err := decodeAdmissionCandidate[T](raw)
	if err != nil {
		return nil, err
	}
	return chain.Run(admission.RequestContext{Context: ctx}, nil, candidate)
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
		return errInvalidAdmissionRequest
	}
	if err := decoder.Decode(&struct{}{}); err != io.EOF {
		return errInvalidAdmissionRequest
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
