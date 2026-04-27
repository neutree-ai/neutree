package util

import (
	"os"
	"strings"
	"testing"
)

func TestSGLangK8sTemplateHyphenizesEngineArgs(t *testing.T) {
	for _, p := range []string{
		"../engine/sglang/v0.5.10/templates/kubernetes/default.yaml",
		"../engine/sglang/deepseek-v4-hopper/templates/kubernetes/default.yaml",
	} {
		b, err := os.ReadFile(p)
		if err != nil {
			t.Fatalf("read %s: %v", p, err)
		}
		v := map[string]interface{}{
			"EndpointName": "ep1", "Namespace": "default", "EngineName": "sglang",
			"EngineVersion": "v0.5.10", "RoutingLogic": "rr", "ClusterName": "c1",
			"Workspace": "w1", "Replicas": 1, "ImagePrefix": "neu", "NeutreeVersion": "v1",
			"ImageRepo": "neutree/engine-sglang", "ImageTag": "v0.5.10-ray2.53.0",
			"ModelArgs": map[string]interface{}{
				"path": "/m", "serve_name": "ds-v4", "name": "x", "task": "text-generation",
			},
			"Env":       map[string]string{"SGLANG_DSV4_FP4_EXPERTS": "0"},
			"Resources": map[string]string{"nvidia.com/gpu": "4"},
			"EngineArgs": map[string]interface{}{
				"tp_size":                      4,
				"speculative_algorithm":        "EAGLE",
				"speculative_num_steps":        3,
				"speculative_eagle_topk":       1,
				"speculative_num_draft_tokens": 4,
				"tool_call_parser":             "deepseekv4",
				"reasoning_parser":             "deepseek-v4",
				"trust_remote_code":            "true",
			},
		}
		objs, err := RenderKubernetesManifest(string(b), v)
		if err != nil {
			t.Fatalf("render %s: %v", p, err)
		}
		var cmds []string
		for _, o := range objs.Items {
			if o.GetKind() != "Deployment" {
				continue
			}
			cur, _ := o.Object["spec"].(map[string]interface{})
			cur, _ = cur["template"].(map[string]interface{})
			cur, _ = cur["spec"].(map[string]interface{})
			conts, _ := cur["containers"].([]interface{})
			for _, c := range conts {
				cm, _ := c.(map[string]interface{})
				if name, _ := cm["name"].(string); name == "sglang" {
					if s, ok := cm["command"].([]interface{}); ok {
						for _, x := range s {
							if str, ok := x.(string); ok {
								cmds = append(cmds, str)
							}
						}
					}
				}
			}
		}
		t.Logf("%s rendered: %s", p, strings.Join(cmds, " "))
		needs := []string{
			"--tp-size", "4",
			"--speculative-algorithm", "EAGLE",
			"--speculative-num-steps", "3",
			"--speculative-eagle-topk", "1",
			"--speculative-num-draft-tokens", "4",
			"--tool-call-parser", "deepseekv4",
			"--reasoning-parser", "deepseek-v4",
			"--trust-remote-code",
		}
		for _, m := range needs {
			if !has(cmds, m) {
				t.Errorf("%s: expected token %q in command, got: %v", p, m, cmds)
			}
		}
		bad := []string{
			"--tp_size", "--speculative_algorithm",
			"--speculative_num_steps", "--speculative_eagle_topk",
			"--speculative_num_draft_tokens",
			"--tool_call_parser", "--reasoning_parser",
			"--trust_remote_code",
		}
		for _, m := range bad {
			if has(cmds, m) {
				t.Errorf("%s: should NOT contain underscore flag %q, got: %v", p, m, cmds)
			}
		}
	}
}

func has(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}
