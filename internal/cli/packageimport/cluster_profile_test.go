package packageimport

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/client"
)

func TestParseManifestFileAcceptsClusterProfile(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), ManifestFileName)
	manifest := `
manifest_version: "1.0"
cluster_profile:
  version: v1.2.0
  components:
    ssh:
      ray_runtime: {image: neutree/neutree-serve, tag: v1.2.0}
      node_agent: {image: neutree/neutree-node-agent, tag: v1.2.0}
      node_exporter: {image: prom/node-exporter, tag: v1.9.1}
      vmagent: {image: victoriametrics/vmagent, tag: v1.115.0}
    kubernetes:
      kubernetes_runtime: {image: neutree/neutree-runtime, tag: v1.2.0}
      router: {image: neutree/neutree-router, tag: v1.2.0}
      node_agent: {image: neutree/neutree-node-agent, tag: v1.2.0}
      node_exporter: {image: prom/node-exporter, tag: v1.9.1}
      vmagent: {image: victoriametrics/vmagent, tag: v1.115.0}
      kube_state_metrics: {image: registry.k8s.io/kube-state-metrics/kube-state-metrics, tag: v2.15.0}
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	parsed, err := NewParser().ParseManifestFile(manifestPath)

	require.NoError(t, err)
	require.NotNil(t, parsed.ClusterProfile)
	assert.Equal(t, "v1.2.0", parsed.ClusterProfile.Version)
	assert.Equal(t, "neutree/neutree-serve", parsed.ClusterProfile.Components[v1.SSHClusterType].RayRuntime.Image)
	assert.Equal(t, "v1.2.0", parsed.ClusterProfile.Components[v1.KubernetesClusterType].Router.Tag)
}

func TestClusterProfileToAPIClusterProfile(t *testing.T) {
	profile := completePackageClusterProfile("v1.2.0")

	converted := profile.ToAPIClusterProfile()

	require.NotNil(t, converted)
	assert.Equal(t, "v1", converted.APIVersion)
	assert.Equal(t, v1.ClusterProfileKind, converted.Kind)
	assert.Equal(t, "v1.2.0", converted.GetName())
	require.NotNil(t, converted.Spec)
	assert.Equal(t, v1.ImageRef{Image: "neutree/neutree-serve", Tag: "v1.2.0"},
		converted.Spec.Components[v1.SSHClusterType].RayRuntime)
	assert.Equal(t, v1.ImageRef{Image: "neutree/neutree-runtime", Tag: "v1.2.0"},
		converted.Spec.Components[v1.KubernetesClusterType].KubernetesRuntime)
}

func TestRegisterManifestRegistersClusterProfileAfterEngines(t *testing.T) {
	var requests []string
	server := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		requests = append(requests, request.Method+" "+request.URL.Path)

		switch request.Method + " " + request.URL.Path {
		case http.MethodGet + " /api/v1/engines":
			_, err := writer.Write([]byte(`[]`))
			require.NoError(t, err)
		case http.MethodPost + " /api/v1/engines":
			writer.WriteHeader(http.StatusCreated)
		case http.MethodPost + " /api/v1/clusters/profile_upsert":
			var payload struct {
				Profile *v1.ClusterProfile `json:"profile"`
			}
			require.NoError(t, json.NewDecoder(request.Body).Decode(&payload))
			require.NotNil(t, payload.Profile)
			assert.Equal(t, "v1.2.0", payload.Profile.GetName())
			assert.Equal(t, "neutree/neutree-serve", payload.Profile.Spec.Components[v1.SSHClusterType].RayRuntime.Image)
			_, err := writer.Write([]byte(`{"operation":"created"}`))
			require.NoError(t, err)
		default:
			http.Error(writer, "unexpected request", http.StatusInternalServerError)
		}
	}))
	t.Cleanup(server.Close)

	importer := NewImporter(client.NewClient(server.URL))
	manifest := &PackageManifest{
		Engines: []*EngineMetadata{{
			Name:           "vllm",
			EngineVersions: []*v1.EngineVersion{{Version: "v0.10.2"}},
		}},
		ClusterProfile: completePackageClusterProfile("v1.2.0"),
	}

	result, err := importer.registerManifest(context.Background(), &ImportOptions{Workspace: "default"}, manifest, &ImportResult{})

	require.NoError(t, err)
	require.NotNil(t, result)
	assert.Equal(t, []string{
		http.MethodGet + " /api/v1/engines",
		http.MethodPost + " /api/v1/engines",
		http.MethodPost + " /api/v1/clusters/profile_upsert",
	}, requests)
}

func TestRegisterManifestDefersOrRequiresClusterProfileClient(t *testing.T) {
	tests := []struct {
		name           string
		manifest       *PackageManifest
		importer       *Importer
		wantFactoryUse int
		wantError      string
	}{
		{
			name:     "profile does not construct client when absent",
			manifest: &PackageManifest{},
		},
		{
			name:           "profile constructs client when present",
			manifest:       &PackageManifest{ClusterProfile: completePackageClusterProfile("v1.2.0")},
			wantFactoryUse: 1,
			wantError:      "API client is required to register cluster profile",
		},
		{
			name:      "profile requires a client",
			manifest:  &PackageManifest{ClusterProfile: completePackageClusterProfile("v1.2.0")},
			importer:  NewImporter(nil),
			wantError: "API client is required to register cluster profile",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			factoryCalls := 0
			importer := tt.importer
			if importer == nil {
				importer = NewImporterWithAPIClientFactory(func() (*client.Client, error) {
					factoryCalls++
					return nil, nil
				})
			}

			_, err := importer.registerManifest(context.Background(), &ImportOptions{}, tt.manifest, &ImportResult{})

			if tt.wantError == "" {
				require.NoError(t, err)
			} else {
				require.EqualError(t, err, tt.wantError)
			}
			assert.Equal(t, tt.wantFactoryUse, factoryCalls)
		})
	}
}

func TestClusterProfileMaterialHandlingValidation(t *testing.T) {
	profileManifest := &PackageManifest{ClusterProfile: completePackageClusterProfile("v1.2.0")}

	tests := []struct {
		name       string
		manifest   *PackageManifest
		opts       *ImportOptions
		standalone bool
		wantError  string
	}{
		{
			name:       "standalone profile requires package URL",
			manifest:   profileManifest,
			opts:       &ImportOptions{},
			standalone: true,
			wantError:  "cluster profile import requires package_url material handling",
		},
		{
			name: "profile cannot skip image load",
			manifest: &PackageManifest{
				Metadata:       &PackageMetadata{PackageURL: "https://example.test/cluster.tar.gz"},
				ClusterProfile: completePackageClusterProfile("v1.2.0"),
			},
			opts:      &ImportOptions{SkipImageLoad: true},
			wantError: "cluster profile import cannot skip image handling",
		},
		{
			name: "archive profile processes image material",
			manifest: &PackageManifest{
				ClusterProfile: completePackageClusterProfile("v1.2.0"),
			},
			opts: &ImportOptions{},
		},
		{
			name:       "engine-only manifest keeps existing skip behavior",
			manifest:   &PackageManifest{},
			opts:       &ImportOptions{SkipImageLoad: true},
			standalone: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateClusterProfileMaterialHandling(tt.manifest, tt.opts, tt.standalone)
			if tt.wantError == "" {
				require.NoError(t, err)
				return
			}

			require.EqualError(t, err, tt.wantError)
		})
	}
}

func TestImportRejectsStandaloneClusterProfileBeforeRegistration(t *testing.T) {
	manifestPath := filepath.Join(t.TempDir(), ManifestFileName)
	manifest := `
manifest_version: "1.0"
cluster_profile:
  version: v1.2.0
`
	require.NoError(t, os.WriteFile(manifestPath, []byte(manifest), 0o600))

	factoryCalls := 0
	importer := NewImporterWithAPIClientFactory(func() (*client.Client, error) {
		factoryCalls++
		return client.NewClient("http://example.invalid"), nil
	})

	_, err := importer.Import(context.Background(), &ImportOptions{
		PackagePath:   manifestPath,
		SkipImagePush: true,
	})

	require.EqualError(t, err, "cluster profile import requires package_url material handling")
	assert.Zero(t, factoryCalls)
}

func completePackageClusterProfile(version string) *ClusterProfile {
	return &ClusterProfile{
		Version: version,
		Components: map[string]ClusterProfileComponents{
			v1.SSHClusterType: {
				RayRuntime:   ClusterImageRef{Image: "neutree/neutree-serve", Tag: version},
				NodeAgent:    ClusterImageRef{Image: "neutree/neutree-node-agent", Tag: version},
				NodeExporter: ClusterImageRef{Image: "prom/node-exporter", Tag: "v1.9.1"},
				VMAgent:      ClusterImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
			},
			v1.KubernetesClusterType: {
				KubernetesRuntime: ClusterImageRef{Image: "neutree/neutree-runtime", Tag: version},
				Router:            ClusterImageRef{Image: "neutree/neutree-router", Tag: version},
				NodeAgent:         ClusterImageRef{Image: "neutree/neutree-node-agent", Tag: version},
				NodeExporter:      ClusterImageRef{Image: "prom/node-exporter", Tag: "v1.9.1"},
				VMAgent:           ClusterImageRef{Image: "victoriametrics/vmagent", Tag: "v1.115.0"},
				KubeStateMetrics:  ClusterImageRef{Image: "registry.k8s.io/kube-state-metrics/kube-state-metrics", Tag: "v2.15.0"},
			},
		},
	}
}
