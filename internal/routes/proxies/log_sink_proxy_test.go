package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestLogSinkMaskedFields(t *testing.T) {
	cfg := extractStructTagConfig(reflect.TypeOf(v1.LogSink{}))

	assert.Equal(t, map[string]struct{}{
		"spec.victorialogs.auth.password": {},
		"spec.victorialogs.auth.token":    {},
	}, cfg.excludeFields)
}

func TestLogSinkRoutesMaskSecretsOnGet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		assert.Equal(t, "/log_sinks", r.URL.Path)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"metadata":{"name":"vl"},
			"spec":{"type":"victorialogs","victorialogs":{
				"url":"https://vl.example.com:9428",
				"auth":{"type":"basic","username":"writer","password":"pw","token":"tk"}
			}}
		}]`))
	}))
	defer upstream.Close()

	router := gin.New()
	RegisterLogSinkRoutes(router.Group("/api/v1"), nil, &Dependencies{StorageAccessURL: upstream.URL})

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log_sinks", nil))

	require.Equal(t, http.StatusOK, recorder.ResponseRecorder.Code)

	var sinks []v1.LogSink
	require.NoError(t, json.Unmarshal(recorder.ResponseRecorder.Body.Bytes(), &sinks))
	require.Len(t, sinks, 1)

	auth := sinks[0].Spec.VictoriaLogs.Auth
	assert.Equal(t, "basic", auth.Type)
	assert.Equal(t, "writer", auth.Username)
	assert.Empty(t, auth.Password)
	assert.Empty(t, auth.Token)
	assert.Equal(t, "https://vl.example.com:9428", sinks[0].Spec.VictoriaLogs.URL)
}

func TestLogSinkRoutesBackfillSecretsOnPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name         string
		body         string
		wantPassword string
		wantToken    string
	}{
		{
			name: "omitted secrets keep stored values",
			body: `{"spec":{"type":"victorialogs","victorialogs":{
				"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer"}}}}`,
			wantPassword: "stored-pw",
			wantToken:    "stored-tk",
		},
		{
			name: "empty secrets keep stored values",
			body: `{"spec":{"type":"victorialogs","victorialogs":{
				"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer","password":""}}}}`,
			wantPassword: "stored-pw",
			wantToken:    "stored-tk",
		},
		{
			name: "provided secret replaces stored value",
			body: `{"spec":{"type":"victorialogs","victorialogs":{
				"url":"https://vl2.example.com","auth":{"type":"basic","username":"writer","password":"new-pw"}}}}`,
			wantPassword: "new-pw",
			wantToken:    "stored-tk",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			storageMock := storageMocks.NewMockStorage(t)
			storageMock.EXPECT().
				GenericQuery(storage.LOG_SINK_TABLE, "spec", mock.Anything, mock.Anything).
				Run(func(_ string, _ string, _ []storage.Filter, result interface{}) {
					*result.(*[]map[string]interface{}) = []map[string]interface{}{{
						"spec": map[string]interface{}{
							"type": "victorialogs",
							"victorialogs": map[string]interface{}{
								"url": "https://vl.example.com",
								"auth": map[string]interface{}{
									"type":     "basic",
									"username": "writer",
									"password": "stored-pw",
									"token":    "stored-tk",
								},
							},
						},
					}}
				}).
				Return(nil)

			var forwarded v1.LogSink
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				assert.Equal(t, http.MethodPatch, r.Method)
				assert.Equal(t, "id=eq.7", r.URL.RawQuery)

				body, err := io.ReadAll(r.Body)
				assert.NoError(t, err)
				assert.NoError(t, json.Unmarshal(body, &forwarded))
				w.WriteHeader(http.StatusNoContent)
			}))
			defer upstream.Close()

			router := gin.New()
			RegisterLogSinkRoutes(router.Group("/api/v1"), nil, &Dependencies{
				StorageAccessURL: upstream.URL,
				Storage:          storageMock,
			})

			req := httptest.NewRequest(http.MethodPatch, "/api/v1/log_sinks?id=eq.7", strings.NewReader(tt.body))
			req.Header.Set("Content-Type", "application/json")

			recorder := newCloseNotifyRecorder()
			router.ServeHTTP(recorder, req)

			require.Equal(t, http.StatusNoContent, recorder.ResponseRecorder.Code)
			require.NotNil(t, forwarded.Spec)
			assert.Equal(t, "https://vl2.example.com", forwarded.Spec.VictoriaLogs.URL)
			assert.Equal(t, tt.wantPassword, forwarded.Spec.VictoriaLogs.Auth.Password)
			assert.Equal(t, tt.wantToken, forwarded.Spec.VictoriaLogs.Auth.Token)
		})
	}
}
