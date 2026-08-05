package clusters

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestProfileUpsert(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name      string
		profile   *v1.ClusterProfile
		force     bool
		mutate    func(*v1.ClusterProfile)
		setup     func(*storageMocks.MockStorage)
		wantCode  int
		operation string
	}{
		{
			name:    "creates missing profile",
			profile: validProfile("v1.2.0-alpha.1"),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{}, nil).Once()
				store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
					return profile.GetName() == "v1.2.0-alpha.1"
				})).Return(nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "created",
		},
		{
			name:    "accepts compatible historical profile",
			profile: validProfile("v1.1.1"),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{}, nil).Once()
				store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
					return profile.GetName() == "v1.1.1"
				})).Return(nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "created",
		},
		{
			name:    "keeps exact duplicate unchanged by default",
			profile: validProfile("v1.2.0-alpha.1"),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{{ID: 21, Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"}}}, nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "unchanged",
		},
		{
			name:    "force updates exact duplicate",
			profile: validProfile("v1.2.0-alpha.1"),
			force:   true,
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{{ID: 21, Metadata: &v1.Metadata{Name: "v1.2.0-alpha.1"}}}, nil).Once()
				store.On("UpdateClusterProfile", "21", mock.Anything).Return(nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "updated",
		},
		{
			name:      "rejects incompatible profile before duplicate decision",
			profile:   validProfile("v1.3.0-alpha.1"),
			setup:     func(*storageMocks.MockStorage) {},
			wantCode:  http.StatusBadRequest,
			operation: "",
		},
		{
			name:    "rejects invalid profile envelope before storage access",
			profile: validProfile("v1.2.0-alpha.1"),
			mutate: func(profile *v1.ClusterProfile) {
				profile.APIVersion = "v2"
			},
			setup:     func(*storageMocks.MockStorage) {},
			wantCode:  http.StatusBadRequest,
			operation: "",
		},
		{
			name:    "rejects workspace profile before storage access",
			profile: validProfile("v1.2.0-alpha.1"),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Metadata.Workspace = "default"
			},
			setup:     func(*storageMocks.MockStorage) {},
			wantCode:  http.StatusBadRequest,
			operation: "",
		},
		{
			name:    "rejects missing component before storage access",
			profile: validProfile("v1.2.0-alpha.1"),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.Components.NodeAgent = v1.ImageRef{}
			},
			setup:     func(*storageMocks.MockStorage) {},
			wantCode:  http.StatusBadRequest,
			operation: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowClusterProfileUpsert(store)
			tt.setup(store)
			if tt.mutate != nil {
				tt.mutate(tt.profile)
			}

			router := gin.New()
			router.Use(func(context *gin.Context) {
				context.Set("user_id", "administrator")
			})
			RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{
				Storage:             store,
				ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()},
			})

			body, err := json.Marshal(map[string]any{"profile": tt.profile, "force_update": tt.force})
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body)))

			require.Equal(t, tt.wantCode, recorder.Code)
			if tt.operation != "" {
				var response struct {
					Operation string `json:"operation"`
				}
				require.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
				assert.Equal(t, tt.operation, response.Operation)
			}
		})
	}
}

func TestProfileUpsertRequiresSystemAdminPermission(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := storageMocks.NewMockStorage(t)
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "unprivileged-user" &&
			params["required_permission"] == "system:admin" &&
			params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = false
	}).Return(nil).Once()

	router := gin.New()
	router.Use(func(context *gin.Context) {
		context.Set("user_id", "unprivileged-user")
	})
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{
		Storage:             store,
		ReleaseInfoProvider: &testReleaseInfoProvider{info: semanticReleaseInfo()},
	})

	body, err := json.Marshal(map[string]any{"profile": validProfile("v1.2.0-alpha.1")})
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body)))

	require.Equal(t, http.StatusForbidden, recorder.Code)
}

func allowClusterProfileUpsert(store *storageMocks.MockStorage) {
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == "administrator" &&
			params["required_permission"] == "system:admin" &&
			params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = true
	}).Return(nil).Once()
}

func validProfile(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec: &v1.ClusterProfileSpec{Components: v1.ClusterProfileComponents{
			RayRuntime:       v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0"},
			Router:           v1.ImageRef{Image: "neutree/router", Tag: "v1.2.0"},
			NodeAgent:        v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0"},
			NodeExporter:     v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
			VMAgent:          v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			KubeStateMetrics: v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
		}},
	}
}
