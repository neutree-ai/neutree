package middleware

import (
	"bytes"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

type releaseInfoProviderFunc func() (*v1.ReleaseInfo, error)

func (fn releaseInfoProviderFunc) Current() (*v1.ReleaseInfo, error) {
	return fn()
}

func TestClusterProfileImportValidation(t *testing.T) {
	profile := completeClusterProfile("v1.1.1")
	validReleaseInfo := currentReleaseInfo()

	tests := []struct {
		name         string
		payload      any
		provider     releaseInfoProviderFunc
		wantStatus   int
		wantContinue bool
		wantCalls    int
	}{
		{
			name:         "allows eligible profile and restores body",
			payload:      map[string]any{"profile": profile},
			provider:     func() (*v1.ReleaseInfo, error) { return validReleaseInfo, nil },
			wantStatus:   http.StatusNoContent,
			wantContinue: true,
			wantCalls:    1,
		},
		{
			name:       "rejects force update true",
			payload:    map[string]any{"profile": profile, "force_update": true},
			provider:   func() (*v1.ReleaseInfo, error) { return validReleaseInfo, nil },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects force update false",
			payload:    map[string]any{"profile": profile, "force_update": false},
			provider:   func() (*v1.ReleaseInfo, error) { return validReleaseInfo, nil },
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects force update null",
			payload:    map[string]any{"profile": profile, "force_update": nil},
			provider:   func() (*v1.ReleaseInfo, error) { return validReleaseInfo, nil },
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "rejects invalid profile through domain validation",
			payload: map[string]any{"profile": &v1.ClusterProfile{
				APIVersion: "v1",
				Kind:       v1.ClusterProfileKind,
				Metadata:   &v1.Metadata{Name: "v1.1.1"},
				Spec:       &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{}},
			}},
			provider:   func() (*v1.ReleaseInfo, error) { return validReleaseInfo, nil },
			wantStatus: http.StatusBadRequest,
			wantCalls:  1,
		},
		{
			name:       "hides current release provider failure",
			payload:    map[string]any{"profile": profile},
			provider:   func() (*v1.ReleaseInfo, error) { return nil, errors.New("database unavailable") },
			wantStatus: http.StatusInternalServerError,
			wantCalls:  1,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body, err := json.Marshal(tt.payload)
			require.NoError(t, err)

			providerCalls := 0
			provider := releaseInfoProviderFunc(func() (*v1.ReleaseInfo, error) {
				providerCalls++
				return tt.provider()
			})

			continued := false
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/profiles", ClusterProfileImportValidation(provider), func(c *gin.Context) {
				continued = true

				validated, found := ClusterProfileImportFromContext(c)
				require.True(t, found)
				assert.Equal(t, profile.GetName(), validated.GetName())

				restoredBody, readErr := io.ReadAll(c.Request.Body)
				require.NoError(t, readErr)
				assert.JSONEq(t, string(body), string(restoredBody))
				c.Status(http.StatusNoContent)
			})

			recorder := httptest.NewRecorder()
			request := httptest.NewRequest(http.MethodPost, "/profiles", bytes.NewReader(body))
			request.Header.Set("Content-Type", "application/json")
			router.ServeHTTP(recorder, request)

			assert.Equal(t, tt.wantStatus, recorder.Code)
			assert.Equal(t, tt.wantContinue, continued)
			assert.Equal(t, tt.wantCalls, providerCalls)
			if tt.wantStatus == http.StatusInternalServerError {
				assert.NotContains(t, recorder.Body.String(), "database unavailable")
			}
		})
	}
}

func currentReleaseInfo() *v1.ReleaseInfo {
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

func completeClusterProfile(version string) *v1.ClusterProfile {
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
