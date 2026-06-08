package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"

	"github.com/neutree-ai/neutree/internal/util"
)

// TestVLLMTemplateTaskTranslation locks in the contract that the
// vLLM K8s deploy templates explicitly translate the Neutree model task
// to vLLM's --runner / --convert flags. Without this, vLLM's auto-detect
// falls back to a generate runner for multimodal embedding architectures
// and silently breaks /v1/embeddings.
func TestVLLMTemplateTaskTranslation(t *testing.T) {
	templates := []struct {
		version  string
		template string
	}{
		{version: "v0.17.1", template: vllmV0_17_1DeployTemplate},
		{version: "v0.22.1", template: vllmV0_22_1DeployTemplate},
	}
	tests := []struct {
		name        string
		task        string
		wantRunner  string // empty = should NOT appear
		wantConvert string // empty = should NOT appear
	}{
		{
			name:        "text-embedding pins pooling runner and embed convert",
			task:        "text-embedding",
			wantRunner:  "pooling",
			wantConvert: "embed",
		},
		{
			name:        "text-rerank pins pooling runner and classify convert",
			task:        "text-rerank",
			wantRunner:  "pooling",
			wantConvert: "classify",
		},
		{
			name:        "text-generation leaves runner/convert at vLLM auto default",
			task:        "text-generation",
			wantRunner:  "",
			wantConvert: "",
		},
	}

	for _, tmpl := range templates {
		t.Run(tmpl.version, func(t *testing.T) {
			for _, tc := range tests {
				t.Run(tc.name, func(t *testing.T) {
					vars := newTestVLLMVars(tmpl.version, tc.task)
					objs, err := util.RenderKubernetesManifest(tmpl.template, vars)
					require.NoError(t, err)
					require.NotEmpty(t, objs.Items)

					deploy := mustFindRenderedObject(t, objs.Items, "Deployment", "ep-test")
					cmd := mustExtractContainerCommand(t, deploy.Object, "vllm-engine")

					assert.Equal(t, tc.wantRunner, flagValue(cmd, "--runner"), "full cmd=%v", cmd)
					assert.Equal(t, tc.wantConvert, flagValue(cmd, "--convert"), "full cmd=%v", cmd)
				})
			}
		})
	}
}

// newTestVLLMVars returns the minimum render variables the vLLM templates
// require. We mirror the shape produced by setModelArgs in the
// kubernetes orchestrator without taking a dependency on that package.
func newTestVLLMVars(version, task string) map[string]any {
	return map[string]any{
		"EndpointName":   "ep-test",
		"Namespace":      "default",
		"EngineName":     "vllm-engine",
		"EngineVersion":  version,
		"RoutingLogic":   "rr",
		"ClusterName":    "test",
		"Workspace":      "ws",
		"Replicas":       1,
		"ImagePrefix":    "registry.test",
		"ImageRepo":      "neutree/engine-vllm",
		"ImageTag":       version,
		"NeutreeVersion": "v0.0.0",
		"ModelArgs": map[string]any{
			"name":          "test-model",
			"registry_type": "hugging-face",
			"registry_path": "",
			"path":          "/tmp/model",
			"version":       "main",
			"file":          "",
			"task":          task,
			"serve_name":    "test-model",
		},
		"Env":       map[string]any{},
		"Resources": map[string]any{},
	}
}

func mustFindRenderedObject(t *testing.T, objs []unstructured.Unstructured, kind, name string) unstructured.Unstructured {
	t.Helper()
	for _, obj := range objs {
		if obj.GetKind() == kind && obj.GetName() == name {
			return obj
		}
	}
	require.Failf(t, "rendered object not found", "%s/%s", kind, name)

	return unstructured.Unstructured{}
}

// mustExtractContainerCommand pulls the named container's command list out of
// a rendered unstructured Deployment object.
func mustExtractContainerCommand(t *testing.T, obj map[string]any, containerName string) []string {
	t.Helper()
	spec := requireMap(t, obj["spec"], "spec")
	tmpl := requireMap(t, spec["template"], "spec.template")
	pod := requireMap(t, tmpl["spec"], "spec.template.spec")
	containers := requireSlice(t, pod["containers"], "spec.template.spec.containers")

	for _, c := range containers {
		cm := requireMap(t, c, "container")
		name, ok := cm["name"].(string)
		if ok && name == containerName {
			cmd := requireSlice(t, cm["command"], "container.command")
			out := make([]string, 0, len(cmd))

			for i, s := range cmd {
				out = append(out, mustString(t, s, "command", i))
			}

			return out
		}
	}
	require.Failf(t, "container not found in rendered deployment", "%q", containerName)

	return nil
}

// flagValue returns the argument that follows the given flag in a command
// list, or "" if the flag isn't present.
func flagValue(cmd []string, flag string) string {
	for i, tok := range cmd {
		if tok == flag && i+1 < len(cmd) {
			return strings.Trim(cmd[i+1], `"`)
		}
	}
	return ""
}

func mustString(t *testing.T, v any, field string, index int) string {
	t.Helper()
	if s, ok := v.(string); ok {
		return s
	}
	require.Failf(t, "field must be string", "%s[%d] got %T (%v)", field, index, v, v)

	return ""
}

func requireMap(t *testing.T, v any, field string) map[string]any {
	t.Helper()
	out, ok := v.(map[string]any)
	require.Truef(t, ok, "%s must be map[string]any, got %T (%v)", field, v, v)

	return out
}

func requireSlice(t *testing.T, v any, field string) []any {
	t.Helper()
	out, ok := v.([]any)
	require.Truef(t, ok, "%s must be []any, got %T (%v)", field, v, v)

	return out
}
