package proxies

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/require"

	"github.com/neutree-ai/neutree/pkg/admission"
)

type admissionPatchWidget struct {
	ID       string `json:"id"`
	Metadata struct {
		Name              string            `json:"name"`
		Workspace         string            `json:"workspace"`
		DeletionTimestamp string            `json:"deletion_timestamp,omitempty"`
		Annotations       map[string]string `json:"annotations,omitempty"`
	} `json:"metadata"`
	Spec struct {
		Value  string `json:"value"`
		Other  string `json:"other,omitempty"`
		Secret string `json:"secret,omitempty" api:"-"`
	} `json:"spec"`
}

var admissionPatchWidgetResource = admission.NewResource[admissionPatchWidget]("admission-patch-widget")

func TestAdmissionPatchReadsOneCallerScopedTargetAndRunsUpdateChain(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default"},"spec":{"value":"before","other":"replaced","secret":"masked"}}`)}}
	chain := &fakePatchAdmissionChain{run: func(_ admission.RequestContext, old, candidate any) (any, error) {
		oldWidget := old.(admissionPatchWidget)
		candidateWidget := candidate.(admissionPatchWidget)
		require.Equal(t, "before", oldWidget.Spec.Value)
		require.Equal(t, "after", candidateWidget.Spec.Value)
		require.Empty(t, oldWidget.Spec.Secret)
		require.Empty(t, candidateWidget.Spec.Secret)
		require.Empty(t, candidateWidget.Spec.Other)
		require.Equal(t, oldWidget.Metadata.Name, candidateWidget.Metadata.Name)
		return candidate, nil
	}}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, body := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"},"unknown":"discarded"}`, func(c *gin.Context) {
		forwarded = true
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		c.Status(http.StatusNoContent)
		_, err = c.Writer.Write(body)
		require.NoError(t, err)
	})

	require.True(t, forwarded)
	require.Equal(t, http.StatusNoContent, status)
	require.JSONEq(t, `{"spec":{"value":"after"}}`, body)
	require.Equal(t, admission.Update, chain.operation)
	require.Equal(t, 1, chain.calls)
	require.Equal(t, "postgrest-jwt", reader.token)
	require.Equal(t, "eq.widget-1", reader.query.Get("id"))
}

func TestAdmissionPatchRejectsNonCanonicalSelectorsBeforeTargetRead(t *testing.T) {
	testCases := []string{
		"/?id=eq.widget-1&id=eq.widget-2",
		"/?id=eq.widget-1&status=eq.active",
		"/?id=gt.widget-1",
		"/?metadata-%3E%3Ename=eq.before",
		"/?metadata-%3E%3Ename=eq.before&metadata-%3E%3Eworkspace=eq.default&limit=1",
	}

	for _, target := range testCases {
		t.Run(target, func(t *testing.T) {
			reader := &fakePatchAdmissionReader{}
			runner := newPatchAdmissionRunner(
				fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, admissionPatchWidgetResource, "widgets",
			)

			forwarded := false
			status, _ := runPatchAdmissionRunner(t, runner, target, `{"spec":{"value":"after"}}`, func(*gin.Context) {
				forwarded = true
			})

			require.Equal(t, http.StatusBadRequest, status)
			require.False(t, forwarded)
			require.Zero(t, reader.calls)
		})
	}
}

func TestPatchAdmissionSelectorsAllowOnlyCanonicalResourceKeys(t *testing.T) {
	testCases := []struct {
		name      string
		query     string
		wantError bool
	}{
		{name: "id", query: "id=eq.role-1"},
		{name: "workspace resource", query: "metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=eq.default"},
		{name: "global role", query: "metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=is.null"},
		{name: "null name", query: "metadata-%3E%3Ename=is.null&metadata-%3E%3Eworkspace=is.null", wantError: true},
		{name: "null id", query: "id=is.null", wantError: true},
		{name: "global role with extra selector", query: "metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=is.null&status=eq.active", wantError: true},
		{name: "non equal workspace", query: "metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=neq.default", wantError: true},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			query, err := url.ParseQuery(testCase.query)
			require.NoError(t, err)

			selectors, err := patchAdmissionSelectors(query)
			if testCase.wantError {
				require.ErrorIs(t, err, errInvalidAdmissionRequest)
				require.Nil(t, selectors)
				return
			}
			require.NoError(t, err)
			require.Equal(t, query, selectors)
		})
	}
}

func TestAdmissionDeleteAllowsGlobalRoleSelectorAndForwards(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":1,"metadata":{"name":"admin"},"spec":{}}`)}}
	chain := &fakePatchAdmissionChain{}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, roleAdmissionResource, "roles",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunner(
		t,
		runner,
		"/?metadata-%3E%3Ename=eq.admin&metadata-%3E%3Eworkspace=is.null",
		`{"metadata":{"name":"admin","deletion_timestamp":"2026-08-05T00:00:00Z"}}`,
		func(*gin.Context) { forwarded = true },
	)

	require.Equal(t, http.StatusOK, status)
	require.True(t, forwarded)
	require.Equal(t, admission.Delete, chain.operation)
	require.Equal(t, 1, chain.calls)
	require.Equal(t, "eq.admin", reader.query.Get("metadata->>name"))
	require.Equal(t, "is.null", reader.query.Get("metadata->>workspace"))
}

func TestAdmissionPatchAllowsReturningResponseParameter(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default"},"spec":{"value":"before"}}`)}}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1&returning=representation", `{"spec":{"value":"after"}}`, func(*gin.Context) {
		forwarded = true
	})

	require.Equal(t, http.StatusOK, status)
	require.True(t, forwarded)
	require.Equal(t, "eq.widget-1", reader.query.Get("id"))
	require.Empty(t, reader.query.Get("returning"))
}

func TestAdmissionPatchMapsCallerScopedTargetCardinality(t *testing.T) {
	testCases := []struct {
		name    string
		targets []json.RawMessage
		want    int
	}{
		{name: "no target", want: http.StatusNotFound},
		{name: "multiple targets", targets: []json.RawMessage{json.RawMessage(`{}`), json.RawMessage(`{}`)}, want: http.StatusConflict},
	}

	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			reader := &fakePatchAdmissionReader{targets: testCase.targets}
			chain := &fakePatchAdmissionChain{}
			runner := newPatchAdmissionRunner(
				fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
			)

			forwarded := false
			status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, func(*gin.Context) {
				forwarded = true
			})

			require.Equal(t, testCase.want, status)
			require.False(t, forwarded)
			require.Zero(t, chain.calls)
		})
	}
}

func TestAdmissionDeleteRunsDeleteChainForAllowedMetadataOnlyChange(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default","annotations":{"keep":"yes"}},"spec":{"value":"before"}}`)}}
	chain := &fakePatchAdmissionChain{run: func(_ admission.RequestContext, old, candidate any) (any, error) {
		oldWidget := old.(admissionPatchWidget)
		candidateWidget := candidate.(admissionPatchWidget)
		require.Empty(t, oldWidget.Metadata.DeletionTimestamp)
		require.Equal(t, "2026-05-28T07:20:48Z", candidateWidget.Metadata.DeletionTimestamp)
		require.Equal(t, "yes", candidateWidget.Metadata.Annotations["keep"])
		require.Equal(t, "true", candidateWidget.Metadata.Annotations["neutree.ai/force-delete"])
		return candidate, nil
	}}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	status, body := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"metadata":{"name":"before","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z","annotations":{"keep":"yes","neutree.ai/force-delete":"true"}}}`, func(c *gin.Context) {
		c.Status(http.StatusNoContent)
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		_, err = c.Writer.Write(body)
		require.NoError(t, err)
	})

	require.Equal(t, http.StatusNoContent, status)
	require.JSONEq(t, `{"metadata":{"name":"before","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z","annotations":{"keep":"yes","neutree.ai/force-delete":"true"}}}`, body)
	require.Equal(t, admission.Delete, chain.operation)
	require.Equal(t, 1, chain.calls)
}

func TestAdmissionPatchUsesUpdateChainForAlreadySoftDeletedTarget(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z"},"spec":{"value":"before"}}`)}}
	chain := &fakePatchAdmissionChain{}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, func(*gin.Context) {
		forwarded = true
	})

	require.Equal(t, http.StatusOK, status)
	require.True(t, forwarded)
	require.Equal(t, admission.Update, chain.operation)
}

func TestAdmissionPatchTrimsAdditivePersistedFieldsBeforeTypedDecode(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default","extra":"not admitted"},"spec":{"value":"before","extra":"not admitted"},"persisted_only":{"opaque":"not admitted"}}`)}}
	chain := &fakePatchAdmissionChain{run: func(_ admission.RequestContext, old, candidate any) (any, error) {
		oldWidget := old.(admissionPatchWidget)
		candidateWidget := candidate.(admissionPatchWidget)
		require.Equal(t, "before", oldWidget.Spec.Value)
		require.Equal(t, "after", candidateWidget.Spec.Value)
		return candidate, nil
	}}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	var forwardedBody string
	status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, func(c *gin.Context) {
		body, err := io.ReadAll(c.Request.Body)
		require.NoError(t, err)
		forwardedBody = string(body)
	})

	require.Equal(t, http.StatusOK, status)
	require.Equal(t, 1, chain.calls)
	require.JSONEq(t, `{"spec":{"value":"after"}}`, forwardedBody)
	require.NotContains(t, forwardedBody, "persisted_only")
}

func TestAdmissionDeleteRejectsAnyChangeOtherThanDeletionFields(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default"},"spec":{"value":"before"}}`)}}
	chain := &fakePatchAdmissionChain{}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"metadata":{"name":"changed","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z"}}`, func(*gin.Context) {
		forwarded = true
	})

	require.Equal(t, http.StatusBadRequest, status)
	require.False(t, forwarded)
	require.Zero(t, chain.calls)
}

func TestAdmissionDeleteRejectsPartialMetadataReplacement(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default","annotations":{"keep":"yes"}},"spec":{"value":"before"}}`)}}
	chain := &fakePatchAdmissionChain{}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"metadata":{"deletion_timestamp":"2026-05-28T07:20:48Z"}}`, func(*gin.Context) {
		forwarded = true
	})

	require.Equal(t, http.StatusBadRequest, status)
	require.False(t, forwarded)
	require.Zero(t, chain.calls)
}

func TestAdmissionPatchRejectsMaskedFieldWriteBeforeAdmission(t *testing.T) {
	for _, requestBody := range []string{
		`{"spec":{"value":"after","secret":"new-secret"}}`,
		`{"metadata":{"name":"before","workspace":"default","deletion_timestamp":"2026-05-28T07:20:48Z"},"spec":{"secret":"new-secret"}}`,
	} {
		reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default"},"spec":{"value":"before","secret":"old-secret"}}`)}}
		chain := &fakePatchAdmissionChain{}
		runner := newPatchAdmissionRunner(
			fakePatchAdmissionResolver{chain: chain}, reader, admissionPatchWidgetResource, "widgets",
		)

		forwarded := false
		status, _ := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", requestBody, func(*gin.Context) {
			forwarded = true
		})

		require.Equal(t, http.StatusBadRequest, status)
		require.False(t, forwarded)
		require.Zero(t, reader.calls)
		require.Zero(t, chain.calls)
	}
}

func TestAdmissionPatchUsesInboundBearerWhenPostgrestTokenIsAbsent(t *testing.T) {
	reader := &fakePatchAdmissionReader{targets: []json.RawMessage{json.RawMessage(`{"id":"widget-1","metadata":{"name":"before","workspace":"default"},"spec":{"value":"before"}}`)}}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, admissionPatchWidgetResource, "widgets",
	)

	forwarded := false
	status, _ := runPatchAdmissionRunnerWithAuth(
		t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, "", "Bearer inbound-jwt", func(*gin.Context) {
			forwarded = true
		},
	)

	require.Equal(t, http.StatusOK, status)
	require.True(t, forwarded)
	require.Equal(t, "inbound-jwt", reader.token)
}

func TestAdmissionPatchRejectsMissingOrMalformedInboundAuthorization(t *testing.T) {
	for _, authorization := range []string{"", "Basic inbound-jwt", "Bearer "} {
		t.Run(authorization, func(t *testing.T) {
			reader := &fakePatchAdmissionReader{}
			runner := newPatchAdmissionRunner(
				fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, admissionPatchWidgetResource, "widgets",
			)

			forwarded := false
			status, body := runPatchAdmissionRunnerWithAuth(
				t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, "", authorization, func(*gin.Context) {
					forwarded = true
				},
			)

			require.Equal(t, http.StatusInternalServerError, status)
			require.JSONEq(t, `{"code":10300,"message":"internal admission error"}`, body)
			require.False(t, forwarded)
			require.Zero(t, reader.calls)
		})
	}
}

func TestAdmissionPatchLogsSanitizedInternalFailureClassification(t *testing.T) {
	previousLogger := recordPatchAdmissionFailure
	t.Cleanup(func() { recordPatchAdmissionFailure = previousLogger })
	var logged patchAdmissionFailure
	recordPatchAdmissionFailure = func(failure patchAdmissionFailure) { logged = failure }

	reader := &fakePatchAdmissionReader{err: errors.New("credential=super-secret&workspace=private")}
	runner := newPatchAdmissionRunner(
		fakePatchAdmissionResolver{chain: &fakePatchAdmissionChain{}}, reader, admissionPatchWidgetResource, "widgets",
	)

	status, body := runPatchAdmissionRunner(t, runner, "/?id=eq.widget-1", `{"spec":{"value":"after"}}`, func(*gin.Context) {})

	require.Equal(t, http.StatusInternalServerError, status)
	require.JSONEq(t, `{"code":10300,"message":"internal admission error"}`, body)
	require.Equal(t, "unexpected", logged.Cause)
	require.Equal(t, "*errors.errorString", logged.ErrorType)
	require.NotContains(t, logged.Cause, "super-secret")
	require.NotContains(t, logged.ErrorType, "super-secret")
}

type fakePatchAdmissionResolver struct {
	chain *fakePatchAdmissionChain
	err   error
}

func (r fakePatchAdmissionResolver) Chain(_ any, operation admission.Operation) (patchAdmissionChain, error) {
	if r.chain != nil {
		r.chain.operation = operation
	}
	return r.chain, r.err
}

type fakePatchAdmissionChain struct {
	calls     int
	operation admission.Operation
	run       func(admission.RequestContext, any, any) (any, error)
}

func (c *fakePatchAdmissionChain) Run(ctx admission.RequestContext, old, candidate any) (any, error) {
	c.calls++
	if c.run == nil {
		return candidate, nil
	}
	return c.run(ctx, old, candidate)
}

type fakePatchAdmissionReader struct {
	calls   int
	token   string
	query   url.Values
	targets []json.RawMessage
	err     error
}

func (r *fakePatchAdmissionReader) Read(_ context.Context, _ string, query url.Values, token string) ([]json.RawMessage, error) {
	r.calls++
	r.token = token
	r.query = query
	return r.targets, r.err
}

func runPatchAdmissionRunner(t *testing.T, runner gin.HandlerFunc, target, body string, next gin.HandlerFunc) (int, string) {
	return runPatchAdmissionRunnerWithAuth(t, runner, target, body, "postgrest-jwt", "", next)
}

func runPatchAdmissionRunnerWithAuth(
	t *testing.T, runner gin.HandlerFunc, target, body, postgrestToken, authorization string, next gin.HandlerFunc,
) (int, string) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.PATCH("/", func(c *gin.Context) {
		if postgrestToken != "" {
			c.Set("postgrest_token", postgrestToken)
		}
	}, runner, next)
	recorder := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, target, bytes.NewBufferString(body))
	req.Header.Set("Content-Type", "application/json")
	if authorization != "" {
		req.Header.Set("Authorization", authorization)
	}
	router.ServeHTTP(recorder, req)
	return recorder.Code, recorder.Body.String()
}
