package proxies

import (
	"bytes"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// newVLStub accepts bearer "good-token" or basic writer/good-pw on the query
// endpoint; /health is open.
func newVLStub(t *testing.T) *httptest.Server {
	t.Helper()

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/health":
			w.WriteHeader(http.StatusOK)
		case "/select/logsql/query":
			user, pw, basic := r.BasicAuth()
			if r.Header.Get("Authorization") == "Bearer good-token" || (basic && user == "writer" && pw == "good-pw") {
				w.WriteHeader(http.StatusOK)
				return
			}

			http.Error(w, "unauthorized", http.StatusUnauthorized)
		default:
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(srv.Close)

	return srv
}

func vlSpec(url string, auth *v1.LogSinkAuth) *v1.LogSinkSpec {
	return &v1.LogSinkSpec{
		Type:         v1.LogSinkTypeVictoriaLogs,
		VictoriaLogs: &v1.VictoriaLogsSinkSpec{URL: url, Auth: auth, TimeoutSeconds: 2},
	}
}

func expectPermission(s *storageMocks.MockStorage, permission string, granted bool) {
	s.On("CallDatabaseFunction", "has_permission",
		mock.MatchedBy(func(params map[string]interface{}) bool {
			return params["user_uuid"] == "user-1" &&
				params["required_permission"] == permission &&
				params["workspace"] == nil
		}), mock.Anything).
		Run(func(args mock.Arguments) {
			*args.Get(2).(*bool) = granted
		}).Return(nil).Once()
}

func callLogSinkTestConnection(t *testing.T, s storage.Storage, userID string, body any) (int, map[string]any) {
	t.Helper()
	gin.SetMode(gin.TestMode)

	router := gin.New()
	setUser := func(c *gin.Context) {
		if userID != "" {
			c.Set("user_id", userID)
		}
	}
	RegisterLogSinkRoutes(router.Group("/api/v1"), []gin.HandlerFunc{setUser}, &Dependencies{Storage: s})

	var raw []byte
	if str, ok := body.(string); ok {
		raw = []byte(str)
	} else {
		var err error
		raw, err = json.Marshal(body)
		require.NoError(t, err)
	}

	w := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/log_sinks/test_connection", bytes.NewReader(raw))
	req.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(w, req)

	var out map[string]any
	require.NoError(t, json.Unmarshal(w.Body.Bytes(), &out), w.Body.String())

	return w.Code, out
}

func TestLogSinkTestConnection(t *testing.T) {
	vl := newVLStub(t)
	id := 7

	storedBearer := &v1.LogSink{ID: id, Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "good-token"})}
	storedBasic := &v1.LogSink{ID: id, Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic, Username: "writer", Password: "good-pw"})}

	tests := []struct {
		name       string
		userID     string
		body       any
		permission string
		granted    bool
		stored     *v1.LogSink
		storedErr  error
		wantCode   int
		wantOK     *bool
		wantErr    string
		wantMsg    string
	}{
		{
			name:       "spec only succeeds",
			body:       logSinkTestConnectionRequest{Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "good-token"})},
			permission: "log_sink:create", granted: true,
			wantCode: http.StatusOK, wantOK: ptr(true),
		},
		{
			name:       "spec only with wrong token is ok=false",
			body:       logSinkTestConnectionRequest{Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "bad"})},
			permission: "log_sink:create", granted: true,
			wantCode: http.StatusOK, wantOK: ptr(false), wantMsg: "authenticated query failed: status 401",
		},
		{
			name:       "unreachable URL is ok=false",
			body:       logSinkTestConnectionRequest{Spec: vlSpec("http://127.0.0.1:1", nil)},
			permission: "log_sink:create", granted: true,
			wantCode: http.StatusOK, wantOK: ptr(false), wantMsg: "health check failed",
		},
		{
			name:       "spec only needs create",
			body:       logSinkTestConnectionRequest{Spec: vlSpec(vl.URL, nil)},
			permission: "log_sink:create", granted: false,
			wantCode: http.StatusForbidden, wantErr: "insufficient permissions",
		},
		{
			name:       "invalid spec is 400",
			body:       logSinkTestConnectionRequest{Spec: vlSpec("ftp://x", nil)},
			permission: "log_sink:create", granted: true,
			wantCode: http.StatusBadRequest, wantErr: "spec.victorialogs.url must be an http(s) URL with a host",
		},
		{
			name:     "malformed body is 400",
			body:     `{"spec":`,
			wantCode: http.StatusBadRequest, wantErr: "invalid request body",
		},
		{
			name:     "empty body is 400",
			body:     map[string]any{},
			wantCode: http.StatusBadRequest, wantErr: "id or spec is required",
		},
		{
			name:     "no user is 401",
			userID:   "-",
			body:     logSinkTestConnectionRequest{ID: &id},
			wantCode: http.StatusUnauthorized, wantErr: "user not authenticated",
		},
		{
			name:       "id only tests stored sink",
			body:       logSinkTestConnectionRequest{ID: &id},
			permission: "log_sink:read", granted: true, stored: storedBearer,
			wantCode: http.StatusOK, wantOK: ptr(true),
		},
		{
			name:       "id only needs read",
			body:       logSinkTestConnectionRequest{ID: &id},
			permission: "log_sink:read", granted: false,
			wantCode: http.StatusForbidden, wantErr: "insufficient permissions",
		},
		{
			name:       "unknown id is 404",
			body:       logSinkTestConnectionRequest{ID: &id},
			permission: "log_sink:read", granted: true, storedErr: storage.ErrResourceNotFound,
			wantCode: http.StatusNotFound, wantErr: "log sink not found",
		},
		{
			name:       "storage failure is 500",
			body:       logSinkTestConnectionRequest{ID: &id},
			permission: "log_sink:read", granted: true, storedErr: errors.New("boom"),
			wantCode: http.StatusInternalServerError, wantErr: "failed to get log sink",
		},
		{
			name: "id and spec backfills blank token",
			body: logSinkTestConnectionRequest{ID: &id,
				Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer})},
			permission: "log_sink:update", granted: true, stored: storedBearer,
			wantCode: http.StatusOK, wantOK: ptr(true),
		},
		{
			name: "id and spec backfills blank password",
			body: logSinkTestConnectionRequest{ID: &id,
				Spec: vlSpec(vl.URL+"/", &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic, Username: "writer"})},
			permission: "log_sink:update", granted: true, stored: storedBasic,
			wantCode: http.StatusOK, wantOK: ptr(true),
		},
		{
			name: "id and spec keeps a supplied token",
			body: logSinkTestConnectionRequest{ID: &id,
				Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer, Token: "bad"})},
			permission: "log_sink:update", granted: true, stored: storedBearer,
			wantCode: http.StatusOK, wantOK: ptr(false), wantMsg: "status 401",
		},
		{
			name: "id and spec does not backfill across auth types",
			body: logSinkTestConnectionRequest{ID: &id,
				Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBasic, Username: "writer"})},
			permission: "log_sink:update", granted: true, stored: storedBearer,
			wantCode: http.StatusOK, wantOK: ptr(false), wantMsg: "status 401",
		},
		{
			name: "id and spec with blank token and stored basic is 400",
			body: logSinkTestConnectionRequest{ID: &id,
				Spec: vlSpec(vl.URL, &v1.LogSinkAuth{Type: v1.LogSinkAuthTypeBearer})},
			permission: "log_sink:update", granted: true, stored: storedBasic,
			wantCode: http.StatusBadRequest, wantErr: "spec.victorialogs.auth.token is required for bearer auth",
		},
		{
			name:       "id and spec needs update",
			body:       logSinkTestConnectionRequest{ID: &id, Spec: vlSpec(vl.URL, nil)},
			permission: "log_sink:update", granted: false,
			wantCode: http.StatusForbidden, wantErr: "insufficient permissions",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			s := storageMocks.NewMockStorage(t)
			if tt.permission != "" {
				expectPermission(s, tt.permission, tt.granted)
			}

			if tt.stored != nil || tt.storedErr != nil {
				var stored *v1.LogSink
				if tt.stored != nil {
					// The handler must not mutate what storage returned.
					b, _ := json.Marshal(tt.stored)
					require.NoError(t, json.Unmarshal(b, &stored))
				}

				s.On("GetLogSink", "7").Return(stored, tt.storedErr).Once()
			}

			userID := "user-1"
			if tt.userID == "-" {
				userID = ""
			}

			code, out := callLogSinkTestConnection(t, s, userID, tt.body)
			assert.Equal(t, tt.wantCode, code, out)

			if tt.wantErr != "" {
				assert.Contains(t, out["error"], tt.wantErr)
				return
			}

			assert.Equal(t, *tt.wantOK, out["ok"])
			assert.Contains(t, out["message"], tt.wantMsg)

			_, err := time.Parse(time.RFC3339, out["time"].(string))
			assert.NoError(t, err)
		})
	}
}

func ptr[T any](v T) *T { return &v }
