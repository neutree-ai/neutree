package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

const (
	callerToken = "caller-token"
	sessionJWT  = "session-jwt"
)

func withCallerToken(c *gin.Context) {
	c.Set("postgrest_token", callerToken)
	c.Next()
}

// callerAuths are the ways a caller reaches the proxy: with an API key the
// auth middleware mints a PostgREST token; a session JWT is forwarded as sent.
var callerAuths = []struct {
	name        string
	middlewares []gin.HandlerFunc
	header      string
}{
	{name: "api key", middlewares: []gin.HandlerFunc{withCallerToken}, header: "sk_test"},
	{name: "session jwt", header: "Bearer " + sessionJWT},
}

func logSinkRow(id int, workspace, password string) map[string]interface{} {
	return map[string]interface{}{
		"id":       id,
		"metadata": map[string]interface{}{"name": "x", "workspace": workspace},
		"spec": map[string]interface{}{
			"type": "victorialogs",
			"victorialogs": map[string]interface{}{
				"url":  "https://vl.example.com",
				"auth": map[string]interface{}{"type": "basic", "username": "writer", "password": password},
			},
		},
	}
}

// callerWorkspace is the only workspace fakeRLSPostgrest lets the caller see.
const callerWorkspace = "a"

// fakeRLSPostgrest holds rows from several workspaces. Reads made with the
// caller's token return only the rows in callerWorkspace; it records the PATCH
// it receives.
type fakeRLSPostgrest struct {
	rows []map[string]interface{}
	// failLookup makes every read fail.
	failLookup bool
	patched    atomic.Int32
	forwarded  []byte
}

func (f *fakeRLSPostgrest) visibleRows() []map[string]interface{} {
	visible := []map[string]interface{}{}

	for _, row := range f.rows {
		if metadata, ok := row["metadata"].(map[string]interface{}); ok && metadata["workspace"] == callerWorkspace {
			visible = append(visible, row)
		}
	}

	return visible
}

func (f *fakeRLSPostgrest) serve(t *testing.T) *httptest.Server {
	t.Helper()

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		if auth := r.Header.Get("Authorization"); auth != "Bearer "+callerToken && auth != "Bearer "+sessionJWT {
			w.WriteHeader(http.StatusUnauthorized)
			_, _ = w.Write([]byte(`{"code":"PGRST301","message":"JWT required"}`))

			return
		}

		switch r.Method {
		case http.MethodGet:
			if f.failLookup {
				w.WriteHeader(http.StatusServiceUnavailable)
				return
			}

			_ = json.NewEncoder(w).Encode(f.visibleRows())
		case http.MethodPatch:
			f.patched.Add(1)
			f.forwarded, _ = io.ReadAll(r.Body)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected %s", r.Method)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func TestPatchBackfillOnlyUsesRowsVisibleToCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)

	// A row with the same name in a workspace the caller cannot see.
	hiddenRow := logSinkRow(2, "b", "secret-of-b")

	tests := []struct {
		name         string
		rows         []map[string]interface{}
		body         string
		wantStatus   int
		wantPatched  bool
		wantPassword string
	}{
		{
			name:         "backfills from the one row the caller can see",
			rows:         []map[string]interface{}{hiddenRow, logSinkRow(1, "a", "secret-of-a")},
			body:         `{"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer"}}}}`,
			wantStatus:   http.StatusNoContent,
			wantPatched:  true,
			wantPassword: "secret-of-a",
		},
		{
			name:         "forwards without backfill when the caller sees no row",
			rows:         []map[string]interface{}{hiddenRow},
			body:         `{"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer"}}}}`,
			wantStatus:   http.StatusNoContent,
			wantPatched:  true,
			wantPassword: "",
		},
		{
			name: "rejects filters matching more than one visible row",
			rows: []map[string]interface{}{
				hiddenRow,
				logSinkRow(1, "a", "secret-of-a"),
				logSinkRow(3, "a", "secret-of-a2"),
			},
			body:       `{"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer"}}}}`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "skips the lookup when no masked column is written",
			rows: []map[string]interface{}{
				hiddenRow,
				logSinkRow(1, "a", "secret-of-a"),
				logSinkRow(3, "a", "secret-of-a2"),
			},
			body:        `{"status":{"phase":"Ready"}}`,
			wantStatus:  http.StatusNoContent,
			wantPatched: true,
		},
	}

	for _, auth := range callerAuths {
		for _, tt := range tests {
			t.Run(auth.name+"/"+tt.name, func(t *testing.T) {
				// Strict: any service-role read fails the test.
				storageMock := storageMocks.NewMockStorage(t)

				fake := &fakeRLSPostgrest{rows: tt.rows}
				upstream := fake.serve(t)

				router := gin.New()
				RegisterLogSinkRoutes(router.Group("/api/v1"), auth.middlewares, &Dependencies{
					StorageAccessURL: upstream.URL,
					Storage:          storageMock,
				})

				req := httptest.NewRequest(http.MethodPatch, "/api/v1/log_sinks?metadata->>name=eq.x", strings.NewReader(tt.body))
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", auth.header)

				recorder := newCloseNotifyRecorder()
				router.ServeHTTP(recorder, req)

				require.Equal(t, tt.wantStatus, recorder.ResponseRecorder.Code, recorder.Body.String())
				assert.NotContains(t, string(fake.forwarded), "secret-of-b")

				if !tt.wantPatched {
					assert.Zero(t, fake.patched.Load())
					return
				}

				require.Equal(t, int32(1), fake.patched.Load())

				var forwarded v1.LogSink
				require.NoError(t, json.Unmarshal(fake.forwarded, &forwarded))

				if forwarded.Spec != nil {
					assert.Equal(t, tt.wantPassword, forwarded.Spec.VictoriaLogs.Auth.Password)
				}
			})
		}
	}
}

func TestClusterPatchValidationOnlyResolvesRowsVisibleToCaller(t *testing.T) {
	gin.SetMode(gin.TestMode)

	cluster := func(id int, workspace string) map[string]interface{} {
		return map[string]interface{}{
			"id":       id,
			"metadata": map[string]interface{}{"name": "x", "workspace": workspace},
			"spec":     map[string]interface{}{"type": "ssh", "version": "v1.0.0", "image_registry": "r"},
		}
	}

	tests := []struct {
		name        string
		rows        []map[string]interface{}
		wantStatus  int
		wantPatched bool
	}{
		{
			name:        "a hidden row with the same name does not block the visible one",
			rows:        []map[string]interface{}{cluster(1, "a"), cluster(2, "b")},
			wantStatus:  http.StatusNoContent,
			wantPatched: true,
		},
		{
			name:       "a row the caller cannot see is not validated against",
			rows:       []map[string]interface{}{cluster(2, "b")},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, auth := range callerAuths {
		for _, tt := range tests {
			t.Run(auth.name+"/"+tt.name, func(t *testing.T) {
				storageMock := storageMocks.NewMockStorage(t)
				storageMock.EXPECT().ListEndpoint(mock.Anything).Return(nil, nil).Maybe()

				fake := &fakeRLSPostgrest{rows: tt.rows}
				upstream := fake.serve(t)

				router := gin.New()
				RegisterClusterRoutes(router.Group("/api/v1"), auth.middlewares, &Dependencies{
					StorageAccessURL: upstream.URL,
					Storage:          storageMock,
				})

				req := httptest.NewRequest(
					http.MethodPatch,
					"/api/v1/clusters?metadata->>name=eq.x",
					strings.NewReader(`{"metadata":{"name":"x","labels":{"k":"v"}}}`),
				)
				req.Header.Set("Content-Type", "application/json")
				req.Header.Set("Authorization", auth.header)

				recorder := newCloseNotifyRecorder()
				router.ServeHTTP(recorder, req)

				assert.Equal(t, tt.wantStatus, recorder.ResponseRecorder.Code, recorder.Body.String())
				assert.Equal(t, tt.wantPatched, fake.patched.Load() == 1)
			})
		}
	}
}

// A PATCH whose target cannot be read must not be forwarded: without the
// stored row the backfill would erase masked fields.
func TestPatchIsNotForwardedWhenTargetLookupFails(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		register func(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies)
		path     string
		body     string
	}{
		{
			name:     "backfill",
			register: RegisterLogSinkRoutes,
			path:     "/api/v1/log_sinks?id=eq.1",
			body:     `{"spec":{"type":"victorialogs","victorialogs":{"url":"https://vl.example.com","auth":{"type":"basic","username":"writer"}}}}`,
		},
		{
			name:     "cluster target",
			register: RegisterClusterRoutes,
			path:     "/api/v1/clusters?id=eq.1",
			body:     `{"metadata":{"name":"x","labels":{"k":"v"}}}`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fake := &fakeRLSPostgrest{failLookup: true}
			upstream := fake.serve(t)

			router := gin.New()
			tt.register(router.Group("/api/v1"), nil, &Dependencies{
				StorageAccessURL: upstream.URL,
				Storage:          storageMocks.NewMockStorage(t),
			})

			req := httptest.NewRequest(http.MethodPatch, tt.path, strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("Authorization", "Bearer "+sessionJWT)

			recorder := newCloseNotifyRecorder()
			router.ServeHTTP(recorder, req)

			assert.Equal(t, http.StatusBadGateway, recorder.ResponseRecorder.Code, recorder.Body.String())
			assert.Contains(t, recorder.Body.String(), targetLookupFailedCode)
			assert.Zero(t, fake.patched.Load())
		})
	}
}

func TestStructProxyDropsRowDetailsFromErrors(t *testing.T) {
	gin.SetMode(gin.TestMode)

	const pgError = `{"code":"23514","message":"new row for relation \"log_sinks\" violates check constraint \"c\"",` +
		`"details":"Failing row contains (7, {\"auth\": {\"password\": \"` + leakedSecret + `\"}}).","hint":null}`

	tests := []struct {
		name        string
		handler     structProxyFactory
		wantDetails bool
	}{
		{
			name:    "resource with masked fields",
			handler: structHandler[v1.LogSink](storage.LOG_SINK_TABLE),
		},
		{
			name:        "resource without masked fields is unchanged",
			handler:     structHandler[v1.Endpoint](storage.ENDPOINT_TABLE),
			wantDetails: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusBadRequest)
				_, _ = w.Write([]byte(pgError))
			}))
			defer upstream.Close()

			recorder := serveQuery(t, tt.handler, &Dependencies{StorageAccessURL: upstream.URL}, http.MethodPost, "")

			require.Equal(t, http.StatusBadRequest, recorder.ResponseRecorder.Code)

			var body map[string]interface{}
			require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
			assert.Equal(t, "23514", body["code"])
			assert.Contains(t, body["message"], "violates check constraint")
			assert.Contains(t, body, "hint")

			if tt.wantDetails {
				assert.Contains(t, body["details"], leakedSecret)
			} else {
				assert.NotContains(t, body, "details")
				assert.NotContains(t, recorder.Body.String(), leakedSecret)
			}
		})
	}
}

// serveBackfillLookup answers a backfill lookup (a GET) with rows and reports
// whether r was one.
func serveBackfillLookup(w http.ResponseWriter, r *http.Request, rows ...map[string]interface{}) bool {
	if r.Method != http.MethodGet {
		return false
	}

	if rows == nil {
		rows = []map[string]interface{}{}
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(rows)

	return true
}
