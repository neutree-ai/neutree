package engine

import (
	"encoding/json"
	"slices"
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

func TestKubernetesTemplatesRenderCLIArgumentActions(t *testing.T) {
	templates := []struct {
		name          string
		template      string
		storeTrueFlag string
		optionalFlag  string
		valueFlag     string
	}{
		{"vllm-v0.17.1", vllmV0_17_1DeployTemplate, "disable-log-stats", "enable-prefix-caching", "revision"},
		{"vllm-v0.24.0", vllmV0_24_0DeployTemplate, "data-parallel-multi-port-external-lb", "enable-prefix-caching", "revision"},
		{"vllm-v0.31.0", vllmV0_31_0DeployTemplate, "aggregate-engine-logging", "enable-prefix-caching", "revision"},
		{"sglang", sglangV0_5_10DeployTemplate, "skip-tokenizer-init", "experts-shared-outer-loras", "debug-tensor-dump-inject"},
	}
	values := []struct {
		name  string
		value any
		text  string
	}{
		{"bool true", true, "true"},
		{"bool false", false, "false"},
		{"string true", "true", "true"},
		{"string false", "false", "false"},
	}

	for _, template := range templates {
		t.Run(template.name, func(t *testing.T) {
			for _, value := range values {
				for _, underscore := range []bool{false, true} {
					key := template.valueFlag
					if underscore {
						key = strings.ReplaceAll(key, "-", "_")
					}
					t.Run(value.name+"/"+key, func(t *testing.T) {
						vars := newTestVLLMVars("test", "text-generation")
						vars["EngineArgs"] = map[string]any{key: value.value}
						command := renderCLICommand(t, template.template, vars)
						flag := "--" + key
						if template.name == "sglang" {
							flag = "--" + template.valueFlag
						}
						assert.Equal(t, value.text, flagValue(command, flag))
					})
				}
			}

			for _, flag := range []string{template.storeTrueFlag, template.optionalFlag, "no-" + template.optionalFlag} {
				for _, value := range values[:4] {
					for _, underscore := range []bool{false, true} {
						key := flag
						if underscore {
							key = strings.ReplaceAll(key, "-", "_")
						}
						t.Run(key+"/"+value.name, func(t *testing.T) {
							vars := newTestVLLMVars("test", "text-generation")
							vars["EngineArgs"] = map[string]any{key: value.value}
							command := renderCLICommand(t, template.template, vars)
							outputKey := key
							if template.name == "sglang" {
								outputKey = flag
							}
							assert.NotContains(t, command, "true")
							assert.NotContains(t, command, "false")
							if value.text == "true" {
								assert.Equal(t, "--"+outputKey, command[len(command)-1])
							} else if flag == template.optionalFlag {
								assert.Equal(t, "--no-"+outputKey, command[len(command)-1])
							} else {
								assert.NotContains(t, command, "--"+outputKey)
								assert.NotContains(t, command, "--no-"+outputKey)
							}
						})
					}
				}
			}
		})
	}
}

func renderCLICommand(t *testing.T, template string, vars map[string]any) []string {
	t.Helper()
	objects, err := util.RenderKubernetesManifest(template, vars)
	require.NoError(t, err)
	deployment := mustFindRenderedObjectByKind(t, objects.Items, "Deployment")
	return mustExtractContainerCommand(t, deployment.Object, "vllm-engine")
}

func TestVLLMTemplatesPreserveHFTokenArgumentTypes(t *testing.T) {
	templates := map[string]string{
		"vllm-v0.17.1": vllmV0_17_1DeployTemplate,
		"vllm-v0.24.0": vllmV0_24_0DeployTemplate,
		"vllm-v0.31.0": vllmV0_31_0DeployTemplate,
	}
	tests := []struct {
		name  string
		value any
		want  []string
	}{
		{"local token", true, []string{"--hf_token"}},
		{"default token behavior", false, nil},
		{"literal true token", "true", []string{"--hf_token", "true"}},
		{"literal false token", "false", []string{"--hf_token", "false"}},
		{"explicit token", "hf_example", []string{"--hf_token", "hf_example"}},
	}
	for name, template := range templates {
		t.Run(name, func(t *testing.T) {
			for _, test := range tests {
				for _, key := range []string{"hf_token", "hf-token"} {
					t.Run(test.name+"/"+key, func(t *testing.T) {
						vars := newTestVLLMVars("test", "text-generation")
						vars["EngineArgs"] = map[string]any{key: test.value}
						command := renderCLICommand(t, template, vars)
						if len(test.want) == 0 {
							assert.NotContains(t, command, "--"+key)
							return
						}
						want := append([]string{}, test.want...)
						want[0] = "--" + key
						require.GreaterOrEqual(t, len(command), len(want))
						assert.Equal(t, want, command[len(command)-len(want):])
					})
				}
			}
		})
	}
}

func TestKubernetesTemplatesRenderSchemaBooleanActions(t *testing.T) {
	templates := []struct {
		name     string
		template string
		schema   []byte
		excluded []string
	}{
		{"vllm-v0.17.1", vllmV0_17_1DeployTemplate, vllmV0_17_1EngineSchema, []string{"enable_mm_processor_stats"}},
		{"vllm-v0.24.0", vllmV0_24_0DeployTemplate, vllmV0_24_0EngineSchema, []string{"enable_mm_processor_stats"}},
		{"vllm-v0.31.0", vllmV0_31_0DeployTemplate, vllmV0_31_0EngineSchema, nil},
		{"sglang", sglangV0_5_10DeployTemplate, sglangV0_5_10EngineSchema, []string{"debug_tensor_dump_inject"}},
	}
	for _, template := range templates {
		t.Run(template.name, func(t *testing.T) {
			var schema struct {
				Properties map[string]struct{ Type any }
			}
			require.NoError(t, json.Unmarshal(template.schema, &schema))
			for _, enabled := range []bool{true, false} {
				args := make(map[string]any)
				for key, property := range schema.Properties {
					if property.Type == "boolean" && !slices.Contains(template.excluded, key) {
						args[key] = enabled
					}
				}
				vars := newTestVLLMVars("test", "text-generation")
				vars["EngineArgs"] = args
				command := renderCLICommand(t, template.template, vars)
				assert.NotContains(t, command, "true")
				assert.NotContains(t, command, "false")
				for key := range args {
					flag := key
					storeTrue := slices.Contains([]string{"disable_log_stats", "aggregate_engine_logging", "data_parallel_multi_port_external_lb"}, key)
					if template.name == "sglang" {
						flag = strings.ReplaceAll(key, "_", "-")
						storeTrue = key != "experts_shared_outer_loras"
					}
					if enabled {
						assert.Contains(t, command, "--"+flag)
					} else if storeTrue {
						assert.NotContains(t, command, "--"+flag)
						assert.NotContains(t, command, "--no-"+flag)
					} else {
						assert.Contains(t, command, "--no-"+flag)
						assert.NotContains(t, command, "--"+flag)
					}
				}
			}
		})
	}
}
