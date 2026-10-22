package engine

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"

	"github.com/neutree-ai/neutree/internal/util"
)

func TestKubernetesTemplatesPreserveEnvironmentValues(t *testing.T) {
	templates := map[string]string{
		"vllm-v0.17.1": vllmV0_17_1DeployTemplate,
		"vllm-v0.24.0": vllmV0_24_0DeployTemplate,
		"vllm-v0.31.0": vllmV0_31_0DeployTemplate,
		"sglang":       sglangV0_5_10DeployTemplate,
		"llama-cpp":    llamaCppDefaultDeployTemplate,
	}
	values := map[string]string{
		"1":          "1",
		"RATIO":      "1:1",
		"QUOTED":     `"1:1"`,
		"JSON":       `{"ratio":"1:1","enabled":true}`,
		"BACKSLASH":  `C:\models\weights`,
		"MULTILINE":  "first\nsecond\tthird\r\nfourth",
		"BOOL":       "false",
		"NUMBER":     "0123",
		"EMPTY":      "",
		"WHITESPACE": "  value  ",
	}

	for name, template := range templates {
		t.Run(name, func(t *testing.T) {
			vars := newTestVLLMVars("test", "text-generation")
			vars["Env"] = values
			vars["ProgressDeadlineSeconds"] = 1200
			vars["StartupProbeFailureThreshold"] = 120
			objects, err := util.RenderKubernetesManifest(template, vars)
			require.NoError(t, err)
			object := mustFindRenderedObjectByKind(t, objects.Items, "Deployment")
			deployment := &appsv1.Deployment{}
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, deployment))
			containers := append([]corev1.Container{}, deployment.Spec.Template.Spec.InitContainers...)
			containers = append(containers, deployment.Spec.Template.Spec.Containers...)
			require.GreaterOrEqual(t, len(containers), 2)

			for _, container := range containers {
				t.Run(container.Name, func(t *testing.T) {
					actual := make(map[string]string, len(container.Env))
					for _, variable := range container.Env {
						actual[variable.Name] = variable.Value
					}
					assert.Equal(t, values, actual)
				})
			}
		})
	}
}

func TestKubernetesTemplatesPassThroughCLIValues(t *testing.T) {
	templates := []struct {
		name     string
		template string
	}{
		{"vllm-v0.17.1", vllmV0_17_1DeployTemplate},
		{"vllm-v0.24.0", vllmV0_24_0DeployTemplate},
		{"vllm-v0.31.0", vllmV0_31_0DeployTemplate},
		{"sglang", sglangV0_5_10DeployTemplate},
	}
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"string true", "true", "true"},
		{"string false", "false", "false"},
		{"integer", 1, "1"},
		{"zero", 0, "0"},
		{"float", 0.85, "0.85"},
		{"string", "main", "main"},
		{"whitespace", " ", " "},
		{"empty string", "", ""},
	}
	keys := []string{"disable_log_stats", "enable_prefix_caching", "no_enable_prefix_caching", "hf_token", "skip_tokenizer_init", "revision"}
	for _, template := range templates {
		t.Run(template.name, func(t *testing.T) {
			for _, key := range keys {
				for _, test := range cases {
					t.Run(key+"/"+test.name, func(t *testing.T) {
						vars := newTestVLLMVars("test", "text-generation")
						vars["EngineArgs"] = map[string]any{key: test.value}
						objects, err := util.RenderKubernetesManifest(template.template, vars)
						require.NoError(t, err)
						deployment := mustFindRenderedObjectByKind(t, objects.Items, "Deployment")
						command := mustExtractContainerCommand(t, deployment.Object, "vllm-engine")
						outputKey := key
						if template.name == "sglang" {
							outputKey = strings.ReplaceAll(key, "_", "-")
						}
						want := []string{"--" + outputKey}
						if test.want != "" {
							want = append(want, test.want)
						}
						require.GreaterOrEqual(t, len(command), len(want))
						assert.Equal(t, want, command[len(command)-len(want):])
					})
				}
			}
		})
	}
}

func TestLlamaCppTemplatePassesThroughCLIValues(t *testing.T) {
	cases := []struct {
		name  string
		value any
		want  string
	}{
		{"bool true", true, "--verbose \"true\""},
		{"bool false", false, "--verbose \"false\""},
		{"empty string", "", "--verbose"},
	}
	for _, test := range cases {
		t.Run(test.name, func(t *testing.T) {
			vars := newTestVLLMVars("test", "text-generation")
			vars["EngineArgs"] = map[string]any{"verbose": test.value}
			vars["ProgressDeadlineSeconds"] = 1200
			vars["StartupProbeFailureThreshold"] = 120
			objects, err := util.RenderKubernetesManifest(llamaCppDefaultDeployTemplate, vars)
			require.NoError(t, err)
			object := mustFindRenderedObjectByKind(t, objects.Items, "Deployment")
			deployment := &appsv1.Deployment{}
			require.NoError(t, runtime.DefaultUnstructuredConverter.FromUnstructured(object.Object, deployment))
			command := strings.TrimSpace(deployment.Spec.Template.Spec.Containers[0].Args[0])
			assert.True(t, strings.HasSuffix(command, test.want), "command: %s", command)
		})
	}
}
