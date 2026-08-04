package proxies

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/admission"
)

type admissionRunnerWidget struct {
	Name string `json:"name"`
}

var admissionRunnerWidgetResource = admission.NewResource[admissionRunnerWidget]("admission-runner-widget")

func TestAdmissionRunnerMutatesThenValidatesAndRestoresObjectBody(t *testing.T) {
	registry := admission.NewRegistry()
	var order []string
	require.NoError(t, registry.RegisterResource(admissionRunnerWidgetResource))
	require.NoError(t, registry.RegisterHook(admissionRunnerWidgetResource, admission.MutateCreate(
		admission.HookMeta{Name: "community.mutate", Order: 1}, 10001,
		func(_ admission.RequestContext, candidate admissionRunnerWidget) (admissionRunnerWidget, error) {
			order = append(order, "mutate")
			candidate.Name += "-mutated"
			return candidate, nil
		},
	)))
	require.NoError(t, registry.RegisterHook(admissionRunnerWidgetResource, admission.ValidateCreate(
		admission.HookMeta{Name: "community.validate", Order: 1}, 10002,
		func(_ admission.RequestContext, candidate admissionRunnerWidget) error {
			order = append(order, "validate")
			if candidate.Name != "input-mutated" {
				return errors.New("validator received unmutated candidate")
			}
			return nil
		},
	)))
	require.NoError(t, registry.Seal())

	var contentLength int64
	status, body := runAdmissionRunnerWithNext(
		t,
		CreateAdmissionRunner(registry, admissionRunnerWidgetResource),
		http.MethodPost,
		`{"name":"input"}`,
		func(c *gin.Context) {
			contentLength = c.Request.ContentLength
			body, err := io.ReadAll(c.Request.Body)
			require.NoError(t, err)
			c.Status(http.StatusNoContent)
			_, err = c.Writer.Write(body)
			require.NoError(t, err)
		},
	)

	require.Equal(t, http.StatusNoContent, status)
	require.JSONEq(t, `{"name":"input-mutated"}`, body)
	require.Equal(t, int64(len(`{"name":"input-mutated"}`)), contentLength)
	require.Equal(t, []string{"mutate", "validate"}, order)
}

func TestAdmissionRunnerRejectsArrayWithoutForwardingBody(t *testing.T) {
	fakeChain := &fakeCreateAdmissionChain{run: func(_ admission.RequestContext, _ any, candidate any) (any, error) {
		widget := candidate.(admissionRunnerWidget)
		if widget.Name == "reject" {
			return nil, &admission.Error{Code: 10001, Message: "rejected", Hint: "correct it"}
		}
		return widget, nil
	}}
	runner := newCreateAdmissionRunner[admissionRunnerWidget](
		fakeCreateAdmissionResolver{chain: fakeChain},
		admissionRunnerWidgetResource,
	)

	forwarded := false
	status, body := runAdmissionRunnerWithNext(
		t,
		runner,
		http.MethodPost,
		`[{"name":"first"},{"name":"reject"},{"name":"after"}]`,
		func(c *gin.Context) {
			forwarded = true
			_, _ = io.ReadAll(c.Request.Body)
		},
	)

	require.False(t, forwarded)
	require.Equal(t, 2, fakeChain.calls)
	require.Equal(t, http.StatusBadRequest, status)
	require.JSONEq(t, `{"code":10001,"message":"rejected","hint":"correct it"}`, body)
}

func TestAdmissionRunnerWritesExpectedAdmissionError(t *testing.T) {
	runner := newCreateAdmissionRunner[admissionRunnerWidget](
		fakeCreateAdmissionResolver{chain: &fakeCreateAdmissionChain{run: func(_ admission.RequestContext, _ any, _ any) (any, error) {
			return nil, &admission.Error{Code: 10001, Message: "rejected"}
		}}},
		admissionRunnerWidgetResource,
	)

	status, body := runAdmissionRunner(t, runner, http.MethodPost, `{"name":"input"}`)

	require.Equal(t, http.StatusBadRequest, status)
	require.JSONEq(t, `{"code":10001,"message":"rejected"}`, body)
}

func TestAdmissionRunnerRedactsUnexpectedErrors(t *testing.T) {
	runner := newCreateAdmissionRunner[admissionRunnerWidget](
		fakeCreateAdmissionResolver{chain: &fakeCreateAdmissionChain{run: func(_ admission.RequestContext, _ any, _ any) (any, error) {
			return nil, errors.New("synthetic unexpected failure")
		}}},
		admissionRunnerWidgetResource,
	)

	status, body := runAdmissionRunner(t, runner, http.MethodPost, `{"name":"input"}`)

	require.Equal(t, http.StatusInternalServerError, status)
	require.JSONEq(t, `{"code":10300,"message":"internal admission error"}`, body)
	require.NotContains(t, body, "synthetic unexpected failure")
}

func TestAdmissionRunnerStopsWhenRequestContextIsCancelled(t *testing.T) {
	fakeChain := &fakeCreateAdmissionChain{run: func(ctx admission.RequestContext, _ any, _ any) (any, error) {
		return nil, ctx.Context.Err()
	}}
	runner := newCreateAdmissionRunner[admissionRunnerWidget](
		fakeCreateAdmissionResolver{chain: fakeChain},
		admissionRunnerWidgetResource,
	)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	status, _ := runAdmissionRunnerWithContext(t, runner, ctx, http.MethodPost, `{"name":"input"}`)

	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, 0, fakeChain.calls)
}

func TestAdmissionRunnerStopsArrayWhenRequestContextIsCancelled(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var fakeChain *fakeCreateAdmissionChain
	fakeChain = &fakeCreateAdmissionChain{run: func(_ admission.RequestContext, _ any, candidate any) (any, error) {
		if fakeChain.calls == 1 {
			cancel()
		}
		return candidate, nil
	}}
	runner := newCreateAdmissionRunner[admissionRunnerWidget](
		fakeCreateAdmissionResolver{chain: fakeChain},
		admissionRunnerWidgetResource,
	)

	status, _ := runAdmissionRunnerWithContext(
		t,
		runner,
		ctx,
		http.MethodPost,
		`[{"name":"first"},{"name":"second"}]`,
	)

	require.Equal(t, http.StatusInternalServerError, status)
	require.Equal(t, 1, fakeChain.calls)
}

type fakeCreateAdmissionResolver struct {
	chain createAdmissionChain
	err   error
}

func (r fakeCreateAdmissionResolver) CreateChain(any) (createAdmissionChain, error) {
	return r.chain, r.err
}

type fakeCreateAdmissionChain struct {
	calls int
	run   func(admission.RequestContext, any, any) (any, error)
}

func (c *fakeCreateAdmissionChain) Run(ctx admission.RequestContext, old, candidate any) (any, error) {
	c.calls++
	return c.run(ctx, old, candidate)
}

func runAdmissionRunner(t *testing.T, runner gin.HandlerFunc, method, requestBody string) (int, string) {
	t.Helper()
	return runAdmissionRunnerWithNext(t, runner, method, requestBody, func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
		_, err = c.Writer.Write(body)
		require.NoError(t, err)
	})
}

func runAdmissionRunnerWithContext(
	t *testing.T, runner gin.HandlerFunc, ctx context.Context, method, requestBody string,
) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, "/", runner, func(c *gin.Context) { c.Status(http.StatusNoContent) })
	recorder := httptest.NewRecorder()
	req := httptest.NewRequestWithContext(ctx, method, "/", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.String()
}

func runAdmissionRunnerWithNext(
	t *testing.T, runner gin.HandlerFunc, method, requestBody string, next gin.HandlerFunc,
) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Handle(method, "/", runner, next)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(method, "/", bytes.NewBufferString(requestBody))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.String()
}
