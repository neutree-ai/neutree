package clusters

import (
	"bytes"
	"encoding/json"
	"errors"
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

type releaseInfoProviderFunc func() (*v1.ReleaseInfo, error)

func (fn releaseInfoProviderFunc) Current() (*v1.ReleaseInfo, error) {
	return fn()
}

func TestProfileUpsertCreatesOrReplaysExactProfile(t *testing.T) {
	tests := []struct {
		name             string
		stored           []v1.ClusterProfile
		request          *v1.ClusterProfile
		wantStatus       int
		wantOperation    string
		wantCreate       bool
		wantConflictBody bool
	}{
		{
			name:          "creates absent profile",
			request:       completeProfile("v1.1.1"),
			wantStatus:    http.StatusOK,
			wantOperation: "created",
			wantCreate:    true,
		},
		{
			name: "replays semantically identical profile",
			stored: []v1.ClusterProfile{func() v1.ClusterProfile {
				profile := completeProfile("v1.1.1")
				profile.ID = 42
				profile.Metadata.CreationTimestamp = "2026-08-01T00:00:00Z"
				profile.Metadata.UpdateTimestamp = "2026-08-02T00:00:00Z"
				return *profile
			}()},
			request:       completeProfile("v1.1.1"),
			wantStatus:    http.StatusOK,
			wantOperation: "unchanged",
		},
		{
			name:   "rejects same version with component drift",
			stored: []v1.ClusterProfile{*completeProfile("v1.1.1")},
			request: func() *v1.ClusterProfile {
				profile := completeProfile("v1.1.1")
				ssh := profile.Spec.Components[v1.SSHClusterType]
				ssh.RayRuntime.Tag = "v1.1.1-rocm"
				profile.Spec.Components[v1.SSHClusterType] = ssh
				return profile
			}(),
			wantStatus:       http.StatusConflict,
			wantConflictBody: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowSystemAdmin(store, "administrator")
			store.On("ListClusterProfile", mock.Anything).Return(tt.stored, nil).Once()
			if tt.wantCreate {
				store.On("CreateClusterProfile", mock.MatchedBy(func(profile *v1.ClusterProfile) bool {
					return profile.GetName() == tt.request.GetName()
				})).Return(nil).Once()
			}

			response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
				return validReleaseInfo(), nil
			}), map[string]any{"profile": tt.request}, "administrator")

			assert.Equal(t, tt.wantStatus, response.Code)
			if tt.wantOperation != "" {
				assert.Contains(t, response.Body.String(), tt.wantOperation)
			}
			if tt.wantConflictBody {
				assert.Contains(t, response.Body.String(), "different content")
			}
		})
	}
}

func TestProfileUpsertRejectsInvalidRequestsBeforeStorage(t *testing.T) {
	tests := []struct {
		name    string
		payload any
	}{
		{
			name:    "force update true",
			payload: map[string]any{"profile": completeProfile("v1.1.1"), "force_update": true},
		},
		{
			name:    "force update false",
			payload: map[string]any{"profile": completeProfile("v1.1.1"), "force_update": false},
		},
		{
			name:    "force update null",
			payload: map[string]any{"profile": completeProfile("v1.1.1"), "force_update": nil},
		},
		{
			name: "incomplete profile",
			payload: map[string]any{"profile": &v1.ClusterProfile{
				APIVersion: "v1",
				Kind:       v1.ClusterProfileKind,
				Metadata:   &v1.Metadata{Name: "v1.1.1"},
				Spec:       &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{}},
			}},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowSystemAdmin(store, "administrator")

			response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
				return validReleaseInfo(), nil
			}), tt.payload, "administrator")

			assert.Equal(t, http.StatusBadRequest, response.Code)
			store.AssertNotCalled(t, "ListClusterProfile", mock.Anything)
			store.AssertNotCalled(t, "CreateClusterProfile", mock.Anything)
		})
	}
}

func TestProfileUpsertRejectsIneligibleVersionBeforeStorage(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	allowSystemAdmin(store, "administrator")

	providerCalls := 0
	response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
		providerCalls++

		return validReleaseInfo(), nil
	}), map[string]any{"profile": completeProfile("v1.2.1")}, "administrator")

	assert.Equal(t, http.StatusBadRequest, response.Code)
	assert.Equal(t, 1, providerCalls)
	store.AssertNotCalled(t, "ListClusterProfile", mock.Anything)
	store.AssertNotCalled(t, "CreateClusterProfile", mock.Anything)
}

func TestProfileUpsertMapsProviderFailureAndPermission(t *testing.T) {
	t.Run("provider failure is internal", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		allowSystemAdmin(store, "administrator")

		response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
			return nil, errors.New("database unavailable")
		}), map[string]any{"profile": completeProfile("v1.1.1")}, "administrator")

		assert.Equal(t, http.StatusInternalServerError, response.Code)
		assert.NotContains(t, response.Body.String(), "database unavailable")
		store.AssertNotCalled(t, "ListClusterProfile", mock.Anything)
	})

	t.Run("requires system admin", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		allowSystemAdminResult(store, "member", false)

		response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
			return validReleaseInfo(), nil
		}), map[string]any{"profile": completeProfile("v1.1.1")}, "member")

		assert.Equal(t, http.StatusForbidden, response.Code)
		store.AssertNotCalled(t, "ListClusterProfile", mock.Anything)
	})
}

func TestProfileUpsertResolvesConcurrentCreateAgainstPersistedProfile(t *testing.T) {
	tests := []struct {
		name          string
		persisted     *v1.ClusterProfile
		wantStatus    int
		wantOperation string
	}{
		{
			name:          "identical profile becomes unchanged",
			persisted:     completeProfile("v1.1.1"),
			wantStatus:    http.StatusOK,
			wantOperation: "unchanged",
		},
		{
			name: "different profile remains conflict",
			persisted: func() *v1.ClusterProfile {
				profile := completeProfile("v1.1.1")
				ssh := profile.Spec.Components[v1.SSHClusterType]
				ssh.RayRuntime.Tag = "v1.1.1-rocm"
				profile.Spec.Components[v1.SSHClusterType] = ssh
				return profile
			}(),
			wantStatus: http.StatusConflict,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			allowSystemAdmin(store, "administrator")
			store.On("ListClusterProfile", mock.Anything).
				Return([]v1.ClusterProfile{}, nil).Once()
			store.On("CreateClusterProfile", mock.Anything).
				Return(errors.New(`{"code":"23505","message":"duplicate key value violates unique constraint"}`)).Once()
			store.On("ListClusterProfile", mock.Anything).
				Return([]v1.ClusterProfile{*tt.persisted}, nil).Once()

			response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
				return validReleaseInfo(), nil
			}), map[string]any{"profile": completeProfile("v1.1.1")}, "administrator")

			assert.Equal(t, tt.wantStatus, response.Code)
			if tt.wantOperation != "" {
				assert.Contains(t, response.Body.String(), tt.wantOperation)
			}
		})
	}
}

func TestProfileUpsertDoesNotMaskNonUniqueCreateFailure(t *testing.T) {
	store := storageMocks.NewMockStorage(t)
	allowSystemAdmin(store, "administrator")
	store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile{}, nil).Once()
	store.On("CreateClusterProfile", mock.Anything).Return(errors.New("database unavailable")).Once()

	response := executeProfileUpsert(t, store, releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
		return validReleaseInfo(), nil
	}), map[string]any{"profile": completeProfile("v1.1.1")}, "administrator")

	assert.Equal(t, http.StatusInternalServerError, response.Code)
	assert.NotContains(t, response.Body.String(), "database unavailable")
	store.AssertNumberOfCalls(t, "ListClusterProfile", 1)
}

func executeProfileUpsert(
	t *testing.T,
	store *storageMocks.MockStorage,
	provider ReleaseInfoProvider,
	payload any,
	userID string,
) *httptest.ResponseRecorder {
	t.Helper()
	gin.SetMode(gin.TestMode)
	router := gin.New()
	router.Use(func(c *gin.Context) { c.Set("user_id", userID) })
	RegisterClusterRoutes(router.Group("/api/v1"), nil, &Dependencies{
		Storage:             store,
		ReleaseInfoProvider: provider,
	})

	body, err := json.Marshal(payload)
	require.NoError(t, err)
	recorder := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodPost, "/api/v1/clusters/profile_upsert", bytes.NewReader(body))
	request.Header.Set("Content-Type", "application/json")
	router.ServeHTTP(recorder, request)

	return recorder
}

func allowSystemAdmin(store *storageMocks.MockStorage, userID string) {
	allowSystemAdminResult(store, userID, true)
}

func allowSystemAdminResult(store *storageMocks.MockStorage, userID string, allowed bool) {
	store.On("CallDatabaseFunction", "has_permission", mock.MatchedBy(func(params map[string]interface{}) bool {
		return params["user_uuid"] == userID && params["required_permission"] == "system:admin" && params["workspace"] == nil
	}), mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = allowed
	}).Return(nil).Once()
}

func validReleaseInfo() *v1.ReleaseInfo {
	return &v1.ReleaseInfo{
		APIVersion: "v1",
		Kind:       v1.ReleaseInfoKind,
		Metadata:   &v1.Metadata{Name: "v1.2.0"},
		Spec: &v1.ReleaseInfoSpec{
			DefaultClusterVersion:      "v1.2.0",
			CompatibleClusterBaselines: []string{"v1.1", "v1.2"},
		},
	}
}

func completeProfile(version string) *v1.ClusterProfile {
	return &v1.ClusterProfile{
		APIVersion: "v1",
		Kind:       v1.ClusterProfileKind,
		Metadata:   &v1.Metadata{Name: version},
		Spec: &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   v1.ImageRef{Image: "neutree/neutree-serve", Tag: version},
				NodeAgent:    v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: version},
				NodeExporter: v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:      v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: v1.ImageRef{Image: "neutree/neutree-runtime", Tag: version},
				Router:            v1.ImageRef{Image: "neutree/router", Tag: version},
				NodeAgent:         v1.ImageRef{Image: "neutree/neutree-node-agent", Tag: version},
				NodeExporter:      v1.ImageRef{Image: "quay.io/prometheus/node-exporter", Tag: "v1.8.2"},
				VMAgent:           v1.ImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
				KubeStateMetrics:  v1.ImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
			},
		}},
	}
}
