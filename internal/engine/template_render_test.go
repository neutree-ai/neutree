package engine

import (
	"strings"
	"testing"

	"github.com/neutree-ai/neutree/internal/util"
)

// TestVLLMV0_17_1TemplateTaskTranslation locks in the contract that the
// v0.17.1 K8s deploy template explicitly translates the Neutree model task
// to vLLM's --runner / --convert flags. Without this, vLLM's auto-detect
// falls back to a generate runner for multimodal embedding architectures
// and silently breaks /v1/embeddings.
func TestVLLMV0_17_1TemplateTaskTranslation(t *testing.T) {
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

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			vars := newTestVLLMV0_17_1Vars(tc.task)
			objs, err := util.RenderKubernetesManifest(vllmV0_17_1DeployTemplate, vars)
			if err != nil {
				t.Fatalf("render failed: %v", err)
			}
			if len(objs.Items) == 0 {
				t.Fatalf("no objects rendered")
			}

			cmd := mustExtractContainerCommand(t, objs.Items[0].Object, "vllm-engine")

			gotRunner := flagValue(cmd, "--runner")
			if gotRunner != tc.wantRunner {
				t.Errorf("--runner: got %q, want %q (full cmd=%v)", gotRunner, tc.wantRunner, cmd)
			}
			gotConvert := flagValue(cmd, "--convert")
			if gotConvert != tc.wantConvert {
				t.Errorf("--convert: got %q, want %q (full cmd=%v)", gotConvert, tc.wantConvert, cmd)
			}
		})
	}
}

// newTestVLLMV0_17_1Vars returns the minimum render variables the v0.17.1
// template requires. We mirror the shape produced by setModelArgs in the
// kubernetes orchestrator without taking a dependency on that package.
func newTestVLLMV0_17_1Vars(task string) map[string]any {
	return map[string]any{
		"EndpointName":   "ep-test",
		"Namespace":      "default",
		"EngineName":     "vllm-engine",
		"EngineVersion":  "v0.17.1",
		"RoutingLogic":   "rr",
		"ClusterName":    "test",
		"Workspace":      "ws",
		"Replicas":       1,
		"ImagePrefix":    "registry.test",
		"ImageRepo":      "neutree/engine-vllm",
		"ImageTag":       "v0.17.1",
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

// mustExtractContainerCommand pulls the named container's command list out of
// a rendered unstructured Deployment object.
func mustExtractContainerCommand(t *testing.T, obj map[string]any, containerName string) []string {
	t.Helper()
	spec, _ := obj["spec"].(map[string]any)
	tmpl, _ := spec["template"].(map[string]any)
	pod, _ := tmpl["spec"].(map[string]any)
	containers, _ := pod["containers"].([]any)
	for _, c := range containers {
		cm, _ := c.(map[string]any)
		if name, _ := cm["name"].(string); name == containerName {
			cmd, _ := cm["command"].([]any)
			out := make([]string, 0, len(cmd))
			for _, s := range cmd {
				out = append(out, asString(s))
			}
			return out
		}
	}
	t.Fatalf("container %q not found in rendered deployment", containerName)
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

func asString(v any) string {
	if s, ok := v.(string); ok {
		return s
	}
	return ""
}
