package util

import (
	"bytes"
	"strings"
	"text/template"

	"github.com/Masterminds/sprig/v3"
	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
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

	// Decode YAML to unstructured objects
	// Note: yaml.NewYAMLOrJSONDecoder is a streaming decoder.
	// Each call to Decode() reads ONE YAML document (separated by ---).
	// We need to loop through all documents in the stream.
	objList := &unstructured.UnstructuredList{}
	decoder := yaml.NewYAMLOrJSONDecoder(&buf, 4096)

	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			// io.EOF indicates end of stream
			if err.Error() == "EOF" {
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
		// Swallow errors inside of a template.
		return ""
	}

	return strings.TrimSuffix(string(data), "\n")
}
