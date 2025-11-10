package util

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"gotest.tools/v3/assert"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
)

// TestRenderKubernetesManifest tests the core logic of RenderKubernetesManifest function
func TestRenderKubernetesManifest(t *testing.T) {
	tests := []struct {
		name     string
		template string
		data     map[string]interface{}
		wantErr  bool
		errMsg   string
		validate func(t *testing.T, objs *unstructured.UnstructuredList)
	}{
		{
			name: "single YAML document without separator",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}`,
			data: map[string]interface{}{
				"name":      "test-config",
				"namespace": "default",
			},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 1)
				assert.Equal(t, "ConfigMap", objs.Items[0].GetKind())
				assert.Equal(t, "test-config", objs.Items[0].GetName())
				assert.Equal(t, "default", objs.Items[0].GetNamespace())
			},
		},
		{
			name: "multiple YAML documents with --- separator",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: config1
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config2
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: config3`,
			data:    map[string]interface{}{},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 3, "Should decode all 3 documents")
				assert.Equal(t, "config1", objs.Items[0].GetName())
				assert.Equal(t, "config2", objs.Items[1].GetName())
				assert.Equal(t, "config3", objs.Items[2].GetName())
			},
		},
		{
			name: "skip empty documents between separators",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: first
---
---
---
apiVersion: v1
kind: ConfigMap
metadata:
  name: second
---
`,
			data:    map[string]interface{}{},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 2, "Should skip empty documents")
				assert.Equal(t, "first", objs.Items[0].GetName())
				assert.Equal(t, "second", objs.Items[1].GetName())
			},
		},
		{
			name: "all empty documents should error",
			template: `---
---

---`,
			data:    map[string]interface{}{},
			wantErr: true,
			errMsg:  "no valid objects found in manifest",
		},
		{
			name: "template variable substitution",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }}
  namespace: {{ .namespace }}
data:
  key1: {{ .value1 }}`,
			data: map[string]interface{}{
				"name":      "test-cm",
				"namespace": "default",
				"value1":    "val1",
			},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 1)
				assert.Equal(t, "test-cm", objs.Items[0].GetName())
				assert.Equal(t, "default", objs.Items[0].GetNamespace())

				data, found, _ := unstructured.NestedMap(objs.Items[0].Object, "data")
				require.True(t, found)
				assert.Equal(t, "val1", data["key1"])
			},
		},
		{
			name: "missing variable uses zero value",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }}
  labels:
    exists: "{{ .exists }}"
    missing: "{{ .missing }}"`,
			data: map[string]interface{}{
				"name":   "test",
				"exists": "yes",
			},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 1)
				labels := objs.Items[0].GetLabels()
				assert.Equal(t, "yes", labels["exists"])
				assert.Equal(t, "<no value>", labels["missing"]) // Go template zero value
			},
		},
		{
			name: "toYaml function integration",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
data:
  config: |
{{ .nested | toYaml | indent 4 }}`,
			data: map[string]interface{}{
				"nested": map[string]interface{}{
					"key1": "value1",
					"key2": map[string]string{
						"subkey": "subvalue",
					},
				},
			},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 1)
				configStr, found, _ := unstructured.NestedString(objs.Items[0].Object, "data", "config")
				require.True(t, found)
				assert.Assert(t, strings.Contains(configStr, "key1: value1"))
				assert.Assert(t, strings.Contains(configStr, "subkey: subvalue"))
			},
		},
		{
			name: "template parse error - unclosed bracket",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }
data:
  key: value`,
			data:    map[string]interface{}{"name": "test"},
			wantErr: true,
			errMsg:  "failed to parse template",
		},
		{
			name: "template parse error - invalid function",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name | nonExistentFunc }}`,
			data:    map[string]interface{}{"name": "test"},
			wantErr: true,
			errMsg:  "failed to parse template",
		},
		{
			name: "YAML decode error - invalid YAML",
			template: `this is not valid YAML
  bad indentation
key: no proper structure`,
			data:    map[string]interface{}{},
			wantErr: true,
			errMsg:  "failed to decode manifest",
		},
		{
			name: "YAML decode error - malformed structure",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: test
  invalid: [unclosed bracket`,
			data:    map[string]interface{}{},
			wantErr: true,
			errMsg:  "failed to decode manifest",
		},
		{
			name:     "empty template string",
			template: ``,
			data:     map[string]interface{}{},
			wantErr:  true,
			errMsg:   "no valid objects found in manifest",
		},
		{
			name:     "only whitespace",
			template: `   `,
			data:     map[string]interface{}{},
			wantErr:  true,
			errMsg:   "no valid objects found in manifest",
		},
		{
			name: "complex data types - lists and nested maps",
			template: `apiVersion: v1
kind: ConfigMap
metadata:
  name: {{ .name }}
data:
  replicas: "{{ .replicas }}"
  enabled: "{{ .enabled }}"
  tags: "{{ index .tags 0 }},{{ index .tags 1 }}"`,
			data: map[string]interface{}{
				"name":     "complex",
				"replicas": 3,
				"enabled":  true,
				"tags":     []string{"tag1", "tag2"},
			},
			wantErr: false,
			validate: func(t *testing.T, objs *unstructured.UnstructuredList) {
				require.Len(t, objs.Items, 1)
				data, found, _ := unstructured.NestedMap(objs.Items[0].Object, "data")
				require.True(t, found)
				assert.Equal(t, "3", data["replicas"])
				assert.Equal(t, "true", data["enabled"])
				assert.Equal(t, "tag1,tag2", data["tags"])
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			objs, err := RenderKubernetesManifest(tt.template, tt.data)

			if tt.wantErr {
				require.Error(t, err)
				if tt.errMsg != "" {
					assert.Assert(t, strings.Contains(err.Error(), tt.errMsg))
				}
				return
			}

			require.NoError(t, err)
			require.NotNil(t, objs)
			if tt.validate != nil {
				tt.validate(t, objs)
			}
		})
	}
}

// TestToYAML tests the core logic of toYAML helper function
func TestToYAML(t *testing.T) {
	tests := []struct {
		name     string
		input    interface{}
		expected string
	}{
		{
			name:     "string type",
			input:    "hello world",
			expected: "hello world",
		},
		{
			name:     "integer type",
			input:    42,
			expected: "42",
		},
		{
			name:     "boolean type - true",
			input:    true,
			expected: "true",
		},
		{
			name:     "boolean type - false",
			input:    false,
			expected: "false",
		},
		{
			name:     "nil value",
			input:    nil,
			expected: "null",
		},
		{
			name: "simple map",
			input: map[string]interface{}{
				"key": "value",
			},
			expected: "key: value",
		},
		{
			name: "nested map structure",
			input: map[string]interface{}{
				"outer": map[string]interface{}{
					"inner": "value",
				},
			},
			expected: "outer:\n  inner: value",
		},
		{
			name:     "simple list",
			input:    []string{"a", "b", "c"},
			expected: "- a\n- b\n- c",
		},
		{
			name:     "list of integers",
			input:    []int{1, 2, 3},
			expected: "- 1\n- 2\n- 3",
		},
		{
			name: "list of maps",
			input: []map[string]string{
				{"name": "item1"},
				{"name": "item2"},
			},
			expected: "- name: item1\n- name: item2",
		},
		{
			name:     "empty string",
			input:    "",
			expected: `""`,
		},
		{
			name:     "string with special chars",
			input:    "hello: world",
			expected: `'hello: world'`,
		},
		{
			name:     "zero integer",
			input:    0,
			expected: "0",
		},
		{
			name:     "empty map",
			input:    map[string]interface{}{},
			expected: "{}",
		},
		{
			name:     "empty slice",
			input:    []string{},
			expected: "[]",
		},
		{
			name: "verify trailing newline removed",
			input: map[string]string{
				"key": "value",
			},
			expected: "key: value", // Should NOT end with \n
		},
		{
			name:     "unmarshalable type - channel",
			input:    make(chan int),
			expected: "", // Should return empty string on error
		},
		{
			name:     "unmarshalable type - function",
			input:    func() {},
			expected: "", // Should return empty string on error
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := toYAML(tt.input)
			assert.Equal(t, tt.expected, result)
		})
	}
}
