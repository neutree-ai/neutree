package util

import (
	"bytes"
	"io"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
	"k8s.io/klog/v2"
	sigyaml "sigs.k8s.io/yaml"
)

func RenderKubernetesManifest(templateStr string, variables any) (*unstructured.UnstructuredList, error) {
	tmpl, err := template.New("manifest").Funcs(sprig.TxtFuncMap()).Funcs(template.FuncMap{
		"toYaml": toYAML,
	}).Parse(templateStr)
	if err != nil {
		return nil, errors.Wrap(err, "failed to parse template")
	}

	var buf bytes.Buffer
	if err := tmpl.Execute(&buf, variables); err != nil {
		return nil, errors.Wrap(err, "failed to execute template")
	}

	return DecodeKubernetesManifest(buf.String())
}

// DecodeKubernetesManifest decodes a multi-document Kubernetes YAML/JSON
// manifest into unstructured objects.
func DecodeKubernetesManifest(manifest string) (*unstructured.UnstructuredList, error) {
	objList := &unstructured.UnstructuredList{}
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewBufferString(manifest), 4096)

	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, errors.Wrap(err, "failed to decode manifest")
		}

		// Skip empty objects (can happen with trailing --- or empty documents)
		if len(obj.Object) == 0 {
			continue
		}

		objList.Items = append(objList.Items, *obj)
	}

	if len(objList.Items) == 0 {
		return nil, errors.New("no valid objects found in manifest")
	}

	return objList, nil
}

func toYAML(v interface{}) string {
	data, err := sigyaml.Marshal(v)
	if err != nil {
		klog.Warningf("Failed to marshal yaml: %v", err)
		// Swallow errors inside of a template.
		return ""
	}

	return strings.TrimSuffix(string(data), "\n")
}
