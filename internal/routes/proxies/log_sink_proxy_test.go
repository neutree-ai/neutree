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
		"spec.s3.auth.access_key_id":      {},
		"spec.s3.auth.secret_access_key":  {},
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

func TestLogSinkRoutesMaskS3KeysOnGet(t *testing.T) {
	gin.SetMode(gin.TestMode)

	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`[{
			"metadata":{"name":"archive"},
			"spec":{"type":"s3","s3":{
				"endpoint":"http://minio:9000","region":"us-east-1","bucket":"traces",
				"auth":{"access_key_id":"ak","secret_access_key":"sk"}
			}}
		}]`))
	}))
	defer upstream.Close()

	router := gin.New()
	RegisterLogSinkRoutes(router.Group("/api/v1"), nil, &Dependencies{StorageAccessURL: upstream.URL})

	recorder := newCloseNotifyRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/api/v1/log_sinks", nil))

	require.Equal(t, http.StatusOK, recorder.ResponseRecorder.Code)
	assert.NotContains(t, recorder.ResponseRecorder.Body.String(), `"ak"`)
	assert.NotContains(t, recorder.ResponseRecorder.Body.String(), `"sk"`)

	var sinks []v1.LogSink
	require.NoError(t, json.Unmarshal(recorder.ResponseRecorder.Body.Bytes(), &sinks))
	require.Len(t, sinks, 1)
	assert.Equal(t, "traces", sinks[0].Spec.S3.Bucket)
}

func TestLogSinkRoutesBackfillS3KeysOnPatch(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		body     string
		wantAK   string
		wantSK   string
		wantNoS3 bool
	}{
		{
			name:   "omitted auth keeps stored keys",
			body:   `{"spec":{"type":"s3","s3":{"region":"us-east-1","bucket":"other"}}}`,
			wantAK: "stored-ak", wantSK: "stored-sk",
		},
		{
			name:   "null auth keeps stored keys",
			body:   `{"spec":{"type":"s3","s3":{"region":"us-east-1","bucket":"other","auth":null}}}`,
			wantAK: "stored-ak", wantSK: "stored-sk",
		},
		{
			name:   "new access key keeps stored secret key",
			body:   `{"spec":{"type":"s3","s3":{"region":"us-east-1","bucket":"other","auth":{"access_key_id":"new-ak","secret_access_key":""}}}}`,
			wantAK: "new-ak", wantSK: "stored-sk",
		},
		{
			name:   "both keys replaced",
			body:   `{"spec":{"type":"s3","s3":{"region":"us-east-1","bucket":"other","auth":{"access_key_id":"new-ak","secret_access_key":"new-sk"}}}}`,
			wantAK: "new-ak", wantSK: "new-sk",
		},
		{
			name:     "PATCH without spec leaves the column alone",
			body:     `{"metadata":{"name":"archive"}}`,
			wantNoS3: true,
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
							"type": "s3",
							"s3": map[string]interface{}{
								"region": "us-east-1",
								"bucket": "traces",
								"auth": map[string]interface{}{
									"access_key_id":     "stored-ak",
									"secret_access_key": "stored-sk",
								},
							},
						},
					}}
				}).
				Return(nil)

			var forwarded v1.LogSink
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

			if tt.wantNoS3 {
				assert.Nil(t, forwarded.Spec)
				return
			}

			require.NotNil(t, forwarded.Spec)
			assert.Equal(t, "other", forwarded.Spec.S3.Bucket)
			require.NotNil(t, forwarded.Spec.S3.Auth)
			assert.Equal(t, tt.wantAK, forwarded.Spec.S3.Auth.AccessKeyID)
			assert.Equal(t, tt.wantSK, forwarded.Spec.S3.Auth.SecretAccessKey)
		})
	}
}
