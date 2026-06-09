package hami

import (
	"bytes"
	"embed"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/pkg/errors"
	"helm.sh/helm/v3/pkg/chart"
	"helm.sh/helm/v3/pkg/chart/loader"
	"helm.sh/helm/v3/pkg/chartutil"
	"helm.sh/helm/v3/pkg/engine"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/util/yaml"
)

const embeddedHAMiChartRoot = "chart/hami"

//go:embed chart/hami/** chart/hami/templates/_commons.tpl chart/hami/templates/_helpers.tpl
var embeddedHAMiChartFS embed.FS

func renderEmbeddedHAMiChart(
	values map[string]interface{},
	namespace string,
	kubeVersion chartutil.KubeVersion,
) (*unstructured.UnstructuredList, error) {
	hamiChart, err := loadEmbeddedHAMiChart()
	if err != nil {
		return nil, err
	}

	capabilities := chartutil.DefaultCapabilities.Copy()
	capabilities.KubeVersion = kubeVersion

	renderValues, err := chartutil.ToRenderValuesWithSchemaValidation(
		hamiChart,
		values,
		chartutil.ReleaseOptions{
			Name:      ChartReleaseName,
			Namespace: namespace,
			Revision:  1,
			IsInstall: true,
		},
		capabilities,
		true,
	)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build HAMi chart render values")
	}

	renderedFiles, err := engine.Engine{}.Render(hamiChart, renderValues)
	if err != nil {
		return nil, errors.Wrap(err, "failed to render HAMi chart")
	}

	manifest := joinRenderedManifests(renderedFiles)
	return decodeRenderedManifest(manifest)
}

func loadEmbeddedHAMiChart() (*chart.Chart, error) {
	files := make([]*loader.BufferedFile, 0)
	err := fs.WalkDir(embeddedHAMiChartFS, embeddedHAMiChartRoot, func(filePath string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			return nil
		}

		data, err := embeddedHAMiChartFS.ReadFile(filePath)
		if err != nil {
			return err
		}

		files = append(files, &loader.BufferedFile{
			Name: strings.TrimPrefix(filePath, embeddedHAMiChartRoot+"/"),
			Data: data,
		})

		return nil
	})
	if err != nil {
		return nil, errors.Wrap(err, "failed to read embedded HAMi chart")
	}

	sort.Slice(files, func(i, j int) bool {
		return files[i].Name < files[j].Name
	})

	hamiChart, err := loader.LoadFiles(files)
	if err != nil {
		return nil, errors.Wrap(err, "failed to load embedded HAMi chart")
	}

	return hamiChart, nil
}

func joinRenderedManifests(renderedFiles map[string]string) string {
	keys := make([]string, 0, len(renderedFiles))
	for key := range renderedFiles {
		keys = append(keys, key)
	}
	sort.Strings(keys)

	var buf bytes.Buffer
	for _, key := range keys {
		content := strings.TrimSpace(renderedFiles[key])
		if content == "" || !isRenderedManifestFile(key) {
			continue
		}

		buf.WriteString("---\n")
		buf.WriteString("# Source: ")
		buf.WriteString(key)
		buf.WriteString("\n")
		buf.WriteString(content)
		buf.WriteString("\n")
	}

	return buf.String()
}

func isRenderedManifestFile(filePath string) bool {
	switch path.Ext(filePath) {
	case ".yaml", ".yml":
		return true
	default:
		return false
	}
}

func decodeRenderedManifest(manifest string) (*unstructured.UnstructuredList, error) {
	decoder := yaml.NewYAMLOrJSONDecoder(bytes.NewBufferString(manifest), 4096)
	objList := &unstructured.UnstructuredList{}

	for {
		obj := &unstructured.Unstructured{}
		if err := decoder.Decode(obj); err != nil {
			if errors.Is(err, io.EOF) {
				break
			}

			return nil, errors.Wrap(err, "failed to decode rendered HAMi manifest")
		}

		if len(obj.Object) == 0 {
			continue
		}

		objList.Items = append(objList.Items, *obj)
	}

	if len(objList.Items) == 0 {
		return nil, errors.New("HAMi chart rendered no Kubernetes objects")
	}

	return objList, nil
}
