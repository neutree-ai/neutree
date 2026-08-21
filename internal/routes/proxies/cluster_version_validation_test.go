package proxies

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestValidateClusterVersionCreateTargetRejectsInvalidInputs(t *testing.T) {
	validInfo := &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec:     &v1.ReleaseInfoSpec{CompatibleClusterBaselines: []string{"v1.2"}},
	}

	tests := []struct {
		name        string
		info        *v1.ReleaseInfo
		version     string
		clusterType string
		listErr     error
		wantErr     string
	}{
		{
			name:        "missing version",
			info:        validInfo,
			clusterType: v1.SSHClusterType,
			wantErr:     "spec.version is required",
		},
		{
			name:        "missing release info fields",
			info:        &v1.ReleaseInfo{},
			version:     "v1.2.0",
			clusterType: v1.SSHClusterType,
			wantErr:     "release info metadata and spec are required",
		},
		{
			name: "invalid current baseline",
			info: &v1.ReleaseInfo{
				Metadata: &v1.Metadata{Name: "invalid"},
				Spec:     &v1.ReleaseInfoSpec{CompatibleClusterBaselines: []string{"v1.2"}},
			},
			version:     "v1.2.0",
			clusterType: v1.SSHClusterType,
			wantErr:     "invalid current control-plane baseline",
		},
		{
			name:        "unsupported cluster type",
			info:        validInfo,
			version:     "v1.2.0",
			clusterType: "custom",
			wantErr:     "unsupported cluster type",
		},
		{
			name:        "profile storage error",
			info:        validInfo,
			version:     "v1.2.0",
			clusterType: v1.SSHClusterType,
			listErr:     errors.New("storage unavailable"),
			wantErr:     "list cluster profiles: storage unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			if tt.listErr != nil {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile(nil), tt.listErr).Once()
			}

			err := validateClusterVersionCreateTarget(store, tt.info, tt.version, tt.clusterType)

			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			store.AssertExpectations(t)
		})
	}
}

func TestValidateClusterVersionUpdateTargetRejectsInvalidInputs(t *testing.T) {
	validInfo := &v1.ReleaseInfo{
		Metadata: &v1.Metadata{Name: "v1.2.0"},
		Spec:     &v1.ReleaseInfoSpec{CompatibleClusterBaselines: []string{"v1.2"}},
	}

	tests := []struct {
		name        string
		current     string
		desired     string
		clusterType string
		listErr     error
		wantErr     string
	}{
		{
			name:        "invalid current version",
			current:     "1.2.0",
			desired:     "v1.2.1",
			clusterType: v1.SSHClusterType,
			wantErr:     "invalid effective current cluster version",
		},
		{
			name:        "invalid desired version",
			current:     "v1.2.0",
			desired:     "1.2.1",
			clusterType: v1.SSHClusterType,
			wantErr:     "invalid desired cluster version",
		},
		{
			name:        "unsupported cluster type",
			current:     "v1.2.0",
			desired:     "v1.2.1",
			clusterType: "custom",
			wantErr:     "unsupported cluster type",
		},
		{
			name:        "profile storage error",
			current:     "v1.2.0",
			desired:     "v1.2.1",
			clusterType: v1.SSHClusterType,
			listErr:     errors.New("storage unavailable"),
			wantErr:     "list cluster profiles: storage unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			if tt.listErr != nil {
				store.On("ListClusterProfile", mock.Anything).Return([]v1.ClusterProfile(nil), tt.listErr).Once()
			}

			err := validateClusterVersionUpdateTarget(store, validInfo, tt.current, tt.desired, tt.clusterType)

			assert.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
			store.AssertExpectations(t)
		})
	}
}

func TestValidateClusterVersionCreateMiddlewareRejectsUnavailableReleaseInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	tests := []struct {
		name     string
		provider ReleaseInfoProvider
		wantHint string
	}{
		{
			name:     "missing provider",
			wantHint: "release info provider is required",
		},
		{
			name:     "provider error",
			provider: &releaseInfoProvider{err: errors.New("release info unavailable")},
			wantHint: "release info unavailable",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			store := storageMocks.NewMockStorage(t)
			proxyCalled := false
			router := gin.New()
			router.POST("/clusters", validateClusterVersionCreate(store, tt.provider), func(c *gin.Context) {
				proxyCalled = true
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			router.ServeHTTP(recorder, httptest.NewRequest(
				http.MethodPost,
				"/clusters",
				strings.NewReader(`{"spec":{"type":"ssh","version":"v1.2.0"}}`),
			))

			assert.Equal(t, http.StatusBadRequest, recorder.Code)
			assert.False(t, proxyCalled)
			var response validationError
			assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
			assert.Equal(t, "10212", response.Code)
			assert.Equal(t, "invalid cluster version create", response.Message)
			assert.Equal(t, tt.wantHint, response.Hint)
			store.AssertExpectations(t)
		})
	}
}

func TestValidateClusterVersionUpdateMiddlewareRejectsUnavailableReleaseInfo(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := storageMocks.NewMockStorage(t)
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Spec:   &v1.ClusterSpec{Type: v1.KubernetesClusterType, Version: "v1.2.0"},
		Status: &v1.ClusterStatus{Version: "v1.2.0"},
	}}, nil).Once()
	proxyCalled := false
	router := gin.New()
	router.PATCH("/clusters", validateClusterVersionUpdate(store, &releaseInfoProvider{
		err: errors.New("release info unavailable"),
	}), func(c *gin.Context) {
		proxyCalled = true
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPatch,
		"/clusters?id=eq.1",
		strings.NewReader(`{"spec":{"version":"v1.2.1"}}`),
	))

	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	assert.False(t, proxyCalled)
	var response validationError
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &response))
	assert.Equal(t, "10212", response.Code)
	assert.Equal(t, "invalid cluster version update", response.Message)
	assert.Equal(t, "release info unavailable", response.Hint)
	store.AssertExpectations(t)
}

func TestValidateClusterVersionUpdateMiddlewareAllowsProviderlessUpgrade(t *testing.T) {
	gin.SetMode(gin.TestMode)

	store := storageMocks.NewMockStorage(t)
	store.On("ListCluster", mock.Anything).Return([]v1.Cluster{{
		Spec:   &v1.ClusterSpec{Type: v1.SSHClusterType, Version: "v1.2.0"},
		Status: &v1.ClusterStatus{Version: "v1.2.0"},
	}}, nil).Once()
	proxyCalled := false
	router := gin.New()
	router.PATCH("/clusters", validateClusterVersionUpdate(store), func(c *gin.Context) {
		proxyCalled = true
		c.Status(http.StatusNoContent)
	})

	recorder := httptest.NewRecorder()
	router.ServeHTTP(recorder, httptest.NewRequest(
		http.MethodPatch,
		"/clusters?id=eq.1",
		strings.NewReader(`{"spec":{"version":"v1.2.1"}}`),
	))

	assert.Equal(t, http.StatusNoContent, recorder.Code)
	assert.True(t, proxyCalled)
	store.AssertExpectations(t)
}

func TestClusterPatchDesiredVersionPayload(t *testing.T) {
	tests := []struct {
		name        string
		payload     string
		wantVersion bool
		wantErr     string
	}{
		{name: "missing spec", payload: `{}`, wantVersion: false},
		{name: "invalid spec", payload: `{"spec":"invalid"}`, wantErr: "cannot unmarshal"},
		{name: "missing version", payload: `{"spec":{}}`, wantVersion: false},
		{name: "invalid version", payload: `{"spec":{"version":1}}`, wantErr: "cannot unmarshal"},
		{name: "valid version", payload: `{"spec":{"version":"v1.2.1"}}`, wantVersion: true},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var payload map[string]json.RawMessage
			assert.NoError(t, json.Unmarshal([]byte(tt.payload), &payload))

			hasVersion, err := clusterPatchDesiredVersionPayload(payload)

			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.wantVersion, hasVersion)
		})
	}
}

func TestClusterAcceleratorVirtualizationDisableRequestedBoundaryInputs(t *testing.T) {
	tests := []struct {
		name    string
		payload string
		want    bool
		wantErr string
	}{
		{name: "invalid payload", payload: `{`, wantErr: "unexpected end of JSON input"},
		{name: "missing spec", payload: `{}`, want: false},
		{name: "invalid accelerator virtualization", payload: `{"spec":{"accelerator_virtualization":"invalid"}}`, wantErr: "cannot unmarshal"},
		{name: "explicit enabled", payload: `{"spec":{"accelerator_virtualization":{"enabled":true}}}`, want: false},
		{name: "invalid enabled", payload: `{"spec":{"accelerator_virtualization":{"enabled":"false"}}}`, wantErr: "cannot unmarshal"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			disabled, err := clusterAcceleratorVirtualizationDisableRequested([]byte(tt.payload))

			if tt.wantErr != "" {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErr)
				return
			}

			assert.NoError(t, err)
			assert.Equal(t, tt.want, disabled)
		})
	}
}

func TestClusterVersionValidationFallbackHelpers(t *testing.T) {
	assert.Equal(t, "v1.2.0", effectiveClusterVersionForUpdate(&v1.Cluster{
		Spec: &v1.ClusterSpec{Version: "v1.2.0"},
	}))
	assert.Equal(t, "v1.2.1", effectiveClusterVersionForUpdate(&v1.Cluster{
		Spec:   &v1.ClusterSpec{Version: "v1.2.0"},
		Status: &v1.ClusterStatus{Version: "v1.2.1"},
	}))
	assert.False(t, clusterMinorAtLeast("invalid", "v1.2"))
	assert.False(t, clusterMinorAtLeast("v1.2", "invalid"))
	assert.ErrorContains(t, requireExactClusterProfile(nil, "v1.2.0", v1.SSHClusterType), "storage is required")
}
