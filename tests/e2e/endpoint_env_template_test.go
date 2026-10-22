package e2e

import (
	"path/filepath"
	"reflect"
	"testing"

	"github.com/stretchr/testify/require"
	"gopkg.in/yaml.v3"
)

func TestEndpointEnvTemplateRoundTrip(t *testing.T) {
	cases := []struct {
		name  string
		key   string
		value string
	}{
		{name: "existing value", key: "E2E_TEST_KEY", value: "e2e_test_value"},
		{name: "ratio", key: "RATIO", value: "1:1"},
		{name: "quoted ratio", key: "QUOTED", value: `"1:1"`},
		{name: "JSON", key: "JSON", value: `{"ratio":"1:1","enabled":true}`},
		{name: "backslash", key: "BACKSLASH", value: `C:\models\weights`},
		{name: "multiline", key: "MULTILINE", value: "first line\nsecond line"},
		{name: "numeric key and value", key: "1", value: "1"},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderTemplate(filepath.Join("testdata", "endpoint.yaml"), map[string]any{
				"E2E_ENDPOINT_NAME":                             "test-endpoint",
				"E2E_WORKSPACE":                                 "default",
				"E2E_CLUSTER_NAME":                              "test-cluster",
				"E2E_ENGINE_NAME":                               "vllm",
				"E2E_ENGINE_VERSION":                            "v0.31.0",
				"E2E_MODEL_REGISTRY":                            "test-registry",
				"E2E_MODEL_NAME":                                "test-model",
				"E2E_MODEL_VERSION":                             "main",
				"E2E_MODEL_TASK":                                "text-generation",
				"E2E_ACCELERATOR_TYPE":                          "nvidia_gpu",
				"E2E_ACCELERATOR_PRODUCT":                       "test-gpu",
				"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_MIB":     "",
				"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_PERCENT": "",
				"E2E_ACCELERATOR_VIRTUALIZATION_CORE_PERCENT":   "",
				"E2E_GPU":                                       "1",
				"E2E_CPU":                                       "4",
				"E2E_MEMORY":                                    "8",
				"E2E_ENGINE_ARGS":                               []EngineArg{{Key: "dtype", Value: "half"}},
				"E2E_ENV":                                       map[string]string{tc.key: tc.value},
				"E2E_REPLICAS_NUM":                              1,
			})
			if err != nil {
				t.Fatalf("render endpoint template: %v", err)
			}

			var endpoint struct {
				Spec struct {
					Env map[any]any `yaml:"env"`
				} `yaml:"spec"`
			}
			if err := yaml.Unmarshal([]byte(rendered), &endpoint); err != nil {
				t.Fatalf("decode endpoint YAML: %v", err)
			}
			want := map[any]any{tc.key: tc.value}
			if !reflect.DeepEqual(endpoint.Spec.Env, want) {
				t.Errorf("env round trip: got %#v, want %#v", endpoint.Spec.Env, want)
			}
		})
	}
}

func TestEndpointEngineArgsTemplateRoundTrip(t *testing.T) {
	cases := []struct {
		name    string
		args    []EngineArg
		want    map[string]any
		omitted []string
	}{
		{
			name:    "K8s vLLM empty flags",
			args:    allSchemaTypesEngineArgsK8s(),
			want:    map[string]any{"enforce_eager": "", "no-enable-prefix-caching": ""},
			omitted: []string{"enable_prefix_caching"},
		},
		{
			name:    "K8s SGLang empty flag",
			args:    allSchemaTypesEngineArgsSGLangK8s(),
			want:    map[string]any{"disable_cuda_graph": ""},
			omitted: []string{"skip_tokenizer_init"},
		},
		{
			name: "SSH vLLM native booleans",
			args: allSchemaTypesEngineArgs(),
			want: map[string]any{"enforce_eager": true, "enable_prefix_caching": false},
		},
		{
			name: "SSH SGLang native booleans",
			args: allSchemaTypesEngineArgsSGLang(),
			want: map[string]any{"disable_cuda_graph": true, "skip_tokenizer_init": false},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			rendered, err := renderTemplate(filepath.Join("testdata", "endpoint.yaml"), map[string]any{
				"E2E_ENDPOINT_NAME":                             "test-endpoint",
				"E2E_WORKSPACE":                                 "default",
				"E2E_CLUSTER_NAME":                              "test-cluster",
				"E2E_ENGINE_NAME":                               "test-engine",
				"E2E_ENGINE_VERSION":                            "test-version",
				"E2E_MODEL_REGISTRY":                            "test-registry",
				"E2E_MODEL_NAME":                                "test-model",
				"E2E_MODEL_VERSION":                             "main",
				"E2E_MODEL_TASK":                                "text-generation",
				"E2E_ACCELERATOR_TYPE":                          "nvidia_gpu",
				"E2E_ACCELERATOR_PRODUCT":                       "test-gpu",
				"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_MIB":     "",
				"E2E_ACCELERATOR_VIRTUALIZATION_MEMORY_PERCENT": "",
				"E2E_ACCELERATOR_VIRTUALIZATION_CORE_PERCENT":   "",
				"E2E_GPU":                                       "1",
				"E2E_CPU":                                       "4",
				"E2E_MEMORY":                                    "8",
				"E2E_ENGINE_ARGS":                               tc.args,
				"E2E_ENV":                                       map[string]string{},
				"E2E_REPLICAS_NUM":                              1,
			})
			require.NoError(t, err)

			var endpoint struct {
				Spec struct {
					Variables struct {
						EngineArgs map[string]any `yaml:"engine_args"`
					} `yaml:"variables"`
				} `yaml:"spec"`
			}
			require.NoError(t, yaml.Unmarshal([]byte(rendered), &endpoint))
			for key, want := range tc.want {
				require.Contains(t, endpoint.Spec.Variables.EngineArgs, key)
				require.Equal(t, want, endpoint.Spec.Variables.EngineArgs[key], "engine arg %s", key)
			}
			for _, key := range tc.omitted {
				require.NotContains(t, endpoint.Spec.Variables.EngineArgs, key)
			}
		})
	}
}
