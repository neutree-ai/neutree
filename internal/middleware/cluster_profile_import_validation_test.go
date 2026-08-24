package middleware

import (
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func TestClusterProfileImportValidation(t *testing.T) {
	profile := completeClusterProfile("v1.1.1")

	tests := []struct {
		name         string
		payload      any
		rawBody      string
		wantStatus   int
		wantContinue bool
		wantProfile  string
	}{
		{
			name:         "allows eligible profile and restores body",
			payload:      map[string]any{"profile": profile},
			wantStatus:   http.StatusNoContent,
			wantContinue: true,
			wantProfile:  profile.GetName(),
		},
		{
			name:       "rejects malformed JSON",
			rawBody:    `{"profile":`,
			wantStatus: http.StatusBadRequest,
		},
		{
			name:       "rejects missing profile",
			payload:    map[string]any{},
			wantStatus: http.StatusBadRequest,
		},
		{
			name:        "rejects force update true",
			payload:     map[string]any{"profile": profile, "force_update": true},
			wantStatus:  http.StatusBadRequest,
			wantProfile: "",
		},
		{
			name:        "rejects force update false",
			payload:     map[string]any{"profile": profile, "force_update": false},
			wantStatus:  http.StatusBadRequest,
			wantProfile: "",
		},
		{
			name:        "rejects force update null",
			payload:     map[string]any{"profile": profile, "force_update": nil},
			wantStatus:  http.StatusBadRequest,
			wantProfile: "",
		},
		{
			name: "rejects incomplete profile",
			payload: map[string]any{"profile": &v1.ClusterProfile{
				APIVersion: "v1",
				Kind:       v1.ClusterProfileKind,
				Metadata:   &v1.Metadata{Name: "v1.1.1"},
				Spec:       &v1.ClusterProfileSpec{Components: map[string]v1.ClusterProfileComponents{}},
			}},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "rejects unsupported component matrix type",
			payload: map[string]any{"profile": func() *v1.ClusterProfile {
				profile := completeClusterProfile("v1.1.1")
				profile.Spec.Components["docker"] = v1.ClusterProfileComponents{}

				return profile
			}()},
			wantStatus: http.StatusBadRequest,
		},
		{
			name: "rejects blank required component tag",
			payload: map[string]any{"profile": func() *v1.ClusterProfile {
				profile := completeClusterProfile("v1.1.1")
				ssh := profile.Spec.Components[v1.SSHClusterType]
				ssh.RayRuntime.Tag = " "
				profile.Spec.Components[v1.SSHClusterType] = ssh

				return profile
			}()},
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			body := []byte(tt.rawBody)
			if tt.rawBody == "" {
				var err error
				body, err = json.Marshal(tt.payload)
				require.NoError(t, err)
			}

			continued := false
			gin.SetMode(gin.TestMode)
			router := gin.New()
			router.POST("/profiles", ClusterProfileImportValidation(), func(c *gin.Context) {
				continued = true

				validated, found := ClusterProfileImportFromContext(c)
				require.True(t, found)
				assert.Equal(t, tt.wantProfile, validated.GetName())

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
		})
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
