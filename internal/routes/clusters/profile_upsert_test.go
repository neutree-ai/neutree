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
		name           string
		profile        *v1.ClusterProfile
		mutate         func(*v1.ClusterProfile)
		forceUpdate    any
		hasForceUpdate bool
		setup          func(*storageMocks.MockStorage)
		wantCode       int
		operation      string
	}{
		{
			name:    "creates missing SSH profile",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{}, nil).Once()
				store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
					return profile.GetName() == "v1.2.0-alpha.1" && profile.GetClusterType() == v1.SSHClusterType
				})).Return(nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "created",
		},
		{
			name:    "creates same version for another cluster type",
			profile: validProfile("v1.2.0-alpha.1", v1.KubernetesClusterType),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{withProfileID(validProfile("v1.2.0-alpha.1", v1.SSHClusterType), 21)}, nil).Once()
				store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
					return profile.GetClusterType() == v1.KubernetesClusterType
				})).Return(nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "created",
		},
		{
			name:    "keeps exact content replay unchanged",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{withProfileID(validProfile("v1.2.0-alpha.1", v1.SSHClusterType), 21)}, nil).Once()
			},
			wantCode:  http.StatusOK,
			operation: "unchanged",
		},
		{
			name:    "rejects different material for same identity",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.Components.RayRuntime.Tag = "v1.2.0-alpha.2"
			},
			setup: func(store *storageMocks.MockStorage) {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{withProfileID(validProfile("v1.2.0-alpha.1", v1.SSHClusterType), 21)}, nil).Once()
			},
			wantCode: http.StatusConflict,
		},
		{
			name:           "rejects true force update escape hatch",
			profile:        validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			forceUpdate:    true,
			hasForceUpdate: true,
			setup:          func(*storageMocks.MockStorage) {},
			wantCode:       http.StatusBadRequest,
		},
		{
			name:           "rejects false force update escape hatch",
			profile:        validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			forceUpdate:    false,
			hasForceUpdate: true,
			setup:          func(*storageMocks.MockStorage) {},
			wantCode:       http.StatusBadRequest,
		},
		{
			name:           "rejects null force update escape hatch",
			profile:        validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			forceUpdate:    nil,
			hasForceUpdate: true,
			setup:          func(*storageMocks.MockStorage) {},
			wantCode:       http.StatusBadRequest,
		},
		{
			name:     "rejects incompatible profile before duplicate decision",
			profile:  validProfile("v1.3.0-alpha.1", v1.SSHClusterType),
			setup:    func(*storageMocks.MockStorage) {},
			wantCode: http.StatusBadRequest,
		},
		{
			name:    "rejects invalid profile envelope before storage access",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			mutate: func(profile *v1.ClusterProfile) {
				profile.APIVersion = "v2"
			},
			setup:    func(*storageMocks.MockStorage) {},
			wantCode: http.StatusBadRequest,
		},
		{
			name:    "rejects missing type before storage access",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.ClusterType = ""
			},
			setup:    func(*storageMocks.MockStorage) {},
			wantCode: http.StatusBadRequest,
		},
		{
			name:    "rejects noncanonical type before storage access",
			profile: validProfile("v1.2.0-alpha.1", v1.SSHClusterType),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.ClusterType = " ssh "
			},
			setup:    func(*storageMocks.MockStorage) {},
			wantCode: http.StatusBadRequest,
		},
		{
			name:    "rejects missing type specific component before storage access",
			profile: validProfile("v1.2.0-alpha.1", v1.KubernetesClusterType),
			mutate: func(profile *v1.ClusterProfile) {
				profile.Spec.Components.KubernetesRuntime = v1.ImageRef{}
			},
			setup:    func(*storageMocks.MockStorage) {},
			wantCode: http.StatusBadRequest,
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

			bodyPayload := map[string]any{"profile": tt.profile}
			if tt.hasForceUpdate {
				bodyPayload["force_update"] = tt.forceUpdate
			}
			body, err := json.Marshal(bodyPayload)
			require.NoError(t, err)
			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body)))

			require.Equal(t, tt.wantCode, recorder.Code)
			if tt.operation != "" {
				var response profileUpsertResponse
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

	body, err := json.Marshal(map[string]any{"profile": validProfile("v1.2.0-alpha.1", v1.SSHClusterType)})
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

func validProfile(version, clusterType string) *v1.ClusterProfile {
	profile := &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec:       &v1.ClusterProfileSpec{ClusterType: clusterType},
	}

	switch clusterType {
	case v1.SSHClusterType:
		profile.Spec.Components = v1.ClusterProfileComponents{
			RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0"},
			NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0"},
			NodeExporter: v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
			VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
		}
	case v1.KubernetesClusterType:
		profile.Spec.Components = v1.ClusterProfileComponents{
			KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.2.0"},
			Router:            v1.ImageRef{Image: "neutree/router", Tag: "v1.2.0"},
			NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: "v1.2.0"},
			NodeExporter:      v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
			VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			KubeStateMetrics:  v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
		}
	}

	return profile
}

func withProfileID(profile *v1.ClusterProfile, id int) v1.ClusterProfile {
	copy := *profile
	copy.ID = id
	return copy
}
