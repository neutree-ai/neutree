package proxies

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/gin-gonic/gin"
	"gopkg.in/yaml.v3"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/recipe"
)

// RegisterModelCatalogRoutes registers model catalog routes
// No fields are masked for this resource
//
// Allowed methods: GET, POST, PATCH
// Disallowed methods:
//   - PUT: Not supported (use PATCH for updates)
//   - DELETE: Use deletion timestamp pattern instead
func RegisterModelCatalogRoutes(group *gin.RouterGroup, middlewares []gin.HandlerFunc, deps *Dependencies) {
	proxyGroup := group.Group("/model_catalogs")
	proxyGroup.Use(middlewares...)

	handler := CreateStructProxyHandler[v1.ModelCatalog](deps, "model_catalogs")

	// Only register allowed methods
	proxyGroup.GET("", handler)
	proxyGroup.POST("", handler)
	proxyGroup.PATCH("", handler)

	// Import endpoint accepts a YAML body (single or multi-doc) or a URL to
	// fetch a recipe from. Each document is parsed independently and reported
	// in the per-item result list.
	proxyGroup.POST("/import", handleImportModelCatalogs(deps))
}

type importRequest struct {
	YAML      string `json:"yaml,omitempty"`
	URL       string `json:"url,omitempty"`
	Workspace string `json:"workspace,omitempty"`
}

type importResultItem struct {
	Index   int    `json:"index"`
	Name    string `json:"name,omitempty"`
	OK      bool   `json:"ok"`
	Error   string `json:"error,omitempty"`
	ID      int    `json:"id,omitempty"`
	Skipped bool   `json:"skipped,omitempty"`
}

type importResponse struct {
	Items []importResultItem `json:"items"`
}

const (
	importMaxFetchBytes = 1 << 20 // 1 MiB recipe payload cap
	importFetchTimeout  = 10 * time.Second
)

var allowedImportSchemes = map[string]struct{}{
	"http":  {},
	"https": {},
}

func handleImportModelCatalogs(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		var req importRequest
		if err := c.ShouldBindJSON(&req); err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "invalid request body: " + err.Error()})
			return
		}

		payload := req.YAML
		if payload == "" && req.URL != "" {
			fetched, err := fetchImportPayload(req.URL)
			if err != nil {
				c.JSON(http.StatusBadGateway, gin.H{"error": err.Error()})
				return
			}

			payload = fetched
		}

		if strings.TrimSpace(payload) == "" {
			c.JSON(http.StatusBadRequest, gin.H{"error": "yaml or url is required"})
			return
		}

		docs, err := splitYAMLDocuments(payload)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": "failed to parse yaml: " + err.Error()})
			return
		}

		if len(docs) == 0 {
			c.JSON(http.StatusBadRequest, gin.H{"error": "no yaml documents found"})
			return
		}

		results := make([]importResultItem, 0, len(docs))

		for i, doc := range docs {
			item := importResultItem{Index: i}

			catalog, name, err := decodeModelCatalogDoc(doc, req.Workspace)
			item.Name = name

			if err != nil {
				item.Error = err.Error()
				results = append(results, item)

				continue
			}

			if err := deps.Storage.CreateModelCatalog(catalog); err != nil {
				item.Error = "storage error: " + err.Error()
				results = append(results, item)

				continue
			}

			item.OK = true
			item.ID = catalog.ID
			results = append(results, item)
		}

		c.JSON(http.StatusOK, importResponse{Items: results})
	}
}

func fetchImportPayload(rawURL string) (string, error) {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return "", fmt.Errorf("invalid url: %w", err)
	}

	if _, ok := allowedImportSchemes[strings.ToLower(parsed.Scheme)]; !ok {
		return "", errors.New("unsupported url scheme; expected http(s)")
	}

	client := &http.Client{Timeout: importFetchTimeout}

	resp, err := client.Get(rawURL)
	if err != nil {
		return "", fmt.Errorf("fetch failed: %w", err)
	}
	defer resp.Body.Close() //nolint:errcheck

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", fmt.Errorf("fetch failed: status %d", resp.StatusCode)
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, importMaxFetchBytes))
	if err != nil {
		return "", fmt.Errorf("read body failed: %w", err)
	}

	return string(body), nil
}

func splitYAMLDocuments(payload string) ([]map[string]any, error) {
	dec := yaml.NewDecoder(strings.NewReader(payload))

	var docs []map[string]any

	for {
		var doc map[string]any

		err := dec.Decode(&doc)
		if errors.Is(err, io.EOF) {
			break
		}

		if err != nil {
			return nil, err
		}

		if len(doc) == 0 {
			continue
		}

		docs = append(docs, doc)
	}

	return docs, nil
}

func decodeModelCatalogDoc(doc map[string]any, defaultWorkspace string) (*v1.ModelCatalog, string, error) {
	kind, _ := doc["kind"].(string)
	if kind != "" && kind != "ModelCatalog" {
		return nil, "", fmt.Errorf("expected kind ModelCatalog, got %q", kind)
	}

	// yaml.v3 decodes nested maps as map[any]any; round-trip
	// through JSON so json.Unmarshal into the typed struct sees pure
	// map[string]any.
	normalized, err := yamlMapToJSONMap(doc)
	if err != nil {
		return nil, "", fmt.Errorf("normalize yaml: %w", err)
	}

	buf, err := json.Marshal(normalized)
	if err != nil {
		return nil, "", fmt.Errorf("marshal json: %w", err)
	}

	var catalog v1.ModelCatalog
	if err := json.Unmarshal(buf, &catalog); err != nil {
		return nil, "", fmt.Errorf("unmarshal catalog: %w", err)
	}

	if catalog.Metadata == nil || strings.TrimSpace(catalog.Metadata.Name) == "" {
		return &catalog, "", errors.New("metadata.name is required")
	}

	if catalog.Metadata.Workspace == "" {
		catalog.Metadata.Workspace = defaultWorkspace
	}

	if catalog.Spec == nil {
		return &catalog, catalog.Metadata.Name, errors.New("spec is required")
	}

	// Recipe MCs declare model per-variant; only require top-level model for
	// trivial MCs (no variants).
	if len(catalog.Spec.Variants) == 0 {
		if catalog.Spec.Model == nil || strings.TrimSpace(catalog.Spec.Model.Name) == "" {
			return &catalog, catalog.Metadata.Name, errors.New("spec.model.name is required")
		}
	}

	if catalog.Spec.Engine == nil || strings.TrimSpace(catalog.Spec.Engine.Engine) == "" {
		return &catalog, catalog.Metadata.Name, errors.New("spec.engine.engine is required")
	}

	if err := recipe.ValidateModelCatalogSpec(catalog.Spec); err != nil {
		return &catalog, catalog.Metadata.Name, err
	}

	catalog.Kind = "ModelCatalog"
	if catalog.APIVersion == "" {
		catalog.APIVersion = "v1"
	}

	return &catalog, catalog.Metadata.Name, nil
}

// yamlMapToJSONMap walks a value tree produced by yaml.v3 (which uses
// map[string]any for !!map but may nest non-string keys in unusual
// cases) and ensures the result round-trips cleanly through encoding/json.
func yamlMapToJSONMap(in any) (any, error) {
	switch v := in.(type) {
	case map[string]any:
		out := make(map[string]any, len(v))

		for k, val := range v {
			conv, err := yamlMapToJSONMap(val)
			if err != nil {
				return nil, err
			}

			out[k] = conv
		}

		return out, nil
	case map[any]any:
		out := make(map[string]any, len(v))

		for k, val := range v {
			ks, ok := k.(string)
			if !ok {
				return nil, fmt.Errorf("non-string map key: %v", k)
			}

			conv, err := yamlMapToJSONMap(val)
			if err != nil {
				return nil, err
			}

			out[ks] = conv
		}

		return out, nil
	case []any:
		out := make([]any, len(v))

		for i, item := range v {
			conv, err := yamlMapToJSONMap(item)
			if err != nil {
				return nil, err
			}

			out[i] = conv
		}

		return out, nil
	default:
		return v, nil
	}
}
