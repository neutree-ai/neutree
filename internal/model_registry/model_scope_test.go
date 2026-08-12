package model_registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

// The stubs below reproduce what www.modelscope.cn was measured to do on
// 2026-08-12; nothing here touches the network.

const modelScopeTestURL = "https://www.modelscope.cn"

// modelScopeSearchRequest is the body the search endpoint receives, decoded so a
// test can assert on the paging the provider chose rather than on a JSON string.
type modelScopeSearchRequest struct {
	PageSize   int    `json:"PageSize"`
	PageNumber int    `json:"PageNumber"`
	Name       string `json:"Name"`
}

// modelScopeCatalogue is a fake hub holding an ordered list of model ids and
// serving them by page, the way the real search endpoint does.
type modelScopeCatalogue struct {
	ids   []string
	total int
	// requests records every (PageSize, PageNumber) asked for, in order.
	requests []modelScopeSearchRequest
	// window mimics the hub's deep-paging limit when non-zero.
	window int
}

func (c *modelScopeCatalogue) client() *http.Client {
	return &http.Client{Transport: &MockRoundTripper{RoundTripFunc: c.roundTrip}}
}

func (c *modelScopeCatalogue) roundTrip(req *http.Request) (*http.Response, error) {
	if req.URL.Path != modelScopeSearchPath {
		return nil, fmt.Errorf("unexpected path %s", req.URL.Path)
	}

	body, err := io.ReadAll(req.Body)
	if err != nil {
		return nil, err
	}

	var decoded modelScopeSearchRequest
	if err := json.Unmarshal(body, &decoded); err != nil {
		return nil, err
	}

	c.requests = append(c.requests, decoded)

	if c.window > 0 && (decoded.PageNumber-1)*decoded.PageSize+decoded.PageSize > c.window {
		// What the hub really answers past the window: a 500, not a 4xx.
		return jsonResponse(http.StatusInternalServerError,
			`{"Code":10010207001,"Message":"list_models_failed","Success":false}`), nil
	}

	start := (decoded.PageNumber - 1) * decoded.PageSize
	end := start + decoded.PageSize

	if start > len(c.ids) {
		start = len(c.ids)
	}

	if end > len(c.ids) {
		end = len(c.ids)
	}

	models := make([]map[string]interface{}, 0, end-start)
	for _, id := range c.ids[start:end] {
		owner, name, _ := strings.Cut(id, "/")
		models = append(models, map[string]interface{}{
			"Path": owner, "Name": name, "CreatedTime": 1745834265,
		})
	}

	total := c.total
	if total == 0 {
		total = len(c.ids)
	}

	payload, err := json.Marshal(map[string]interface{}{
		"Code": 200, "Message": "success", "Success": true,
		"Data": map[string]interface{}{
			"Model": map[string]interface{}{"Models": models, "TotalCount": total},
		},
	})
	if err != nil {
		return nil, err
	}

	return jsonResponse(http.StatusOK, string(payload)), nil
}

func jsonResponse(status int, body string) *http.Response {
	return &http.Response{
		StatusCode: status,
		Body:       io.NopCloser(bytes.NewBufferString(body)),
		Header:     make(http.Header),
	}
}

func catalogueOf(n int) []string {
	ids := make([]string, 0, n)
	for i := 0; i < n; i++ {
		ids = append(ids, fmt.Sprintf("owner/model-%03d", i))
	}

	return ids
}

func modelIDs(page *ModelPage) []string {
	ids := make([]string, 0, len(page.Models))
	for _, model := range page.Models {
		ids = append(ids, model.Name)
	}

	return ids
}

func TestNewModelScope(t *testing.T) {
	tests := []struct {
		name          string
		url           string
		wantErrString string
	}{
		{name: "empty url", url: "", wantErrString: "cannot be empty"},
		{name: "no scheme", url: "invalid-url", wantErrString: "invalid registry.Spec.Url"},
		{name: "no host", url: "http://", wantErrString: "invalid registry.Spec.Url"},
		{name: "unparseable", url: "http://%zz", wantErrString: "invalid registry.Spec.Url"},
		{name: "the hub", url: modelScopeTestURL},
		{name: "a mirror with a trailing slash", url: "https://ms-mirror.example/"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms, err := newModelScope(&v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{Type: v1.ModelScopeModelRegistryType, Url: tt.url},
			})

			if tt.wantErrString != "" {
				require.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrString)

				return
			}

			require.NoError(t, err)
			assert.False(t, strings.HasSuffix(ms.url, "/"), "the base url keeps no trailing slash")
		})
	}
}

// The factory has to know the kind, or a registry stored as model-scope is
// rejected at every read with "unsupported model registry type".
func TestNewModelRegistryBuildsModelScope(t *testing.T) {
	registry, err := new(&v1.ModelRegistry{
		Spec: &v1.ModelRegistrySpec{Type: v1.ModelScopeModelRegistryType, Url: modelScopeTestURL},
	})
	require.NoError(t, err)
	assert.IsType(t, &modelScope{}, registry)
}

// Unlike the Hugging Face Hub, ModelScope pages by row offset, so an offset is
// honoured rather than refused. Measured: a page of ten is exactly two
// consecutive pages of five, same order.
func TestModelScope_ListModelsHonoursAnOffset(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(50), total: 23809}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: 10, Limit: 5})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"owner/model-010", "owner/model-011", "owner/model-012",
		"owner/model-013", "owner/model-014",
	}, modelIDs(page))

	// An aligned offset is one page number, so it costs exactly one request.
	require.Len(t, catalogue.requests, 1)
	assert.Equal(t, modelScopeSearchRequest{PageSize: 5, PageNumber: 3}, catalogue.requests[0])
}

// The hub states how many models matched, so that number is passed through
// instead of being reported as unknown the way the Hub's is.
func TestModelScope_ListModelsReportsTheRealTotal(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(50), total: 23809}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Limit: 5})
	require.NoError(t, err)

	require.NotNil(t, page.Total, "ModelScope reports a match count; it must not be dropped")
	assert.Equal(t, 23809, *page.Total)
	// Total is what matched, not what was returned.
	assert.Len(t, page.Models, 5)
}

// Paging is only useful if consecutive pages tile the catalogue: no repeats, no
// gaps. This is the property the offset support exists for.
func TestModelScope_ConsecutivePagesTileTheCatalogue(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(50)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	var walked []string

	for offset := 0; offset < 20; offset += 5 {
		page, err := ms.ListModels(ListOption{Offset: offset, Limit: 5})
		require.NoError(t, err)
		walked = append(walked, modelIDs(page)...)
	}

	assert.Equal(t, catalogueOf(50)[:20], walked)
}

// An offset that is not a whole number of pages cannot be asked for directly,
// because the hub pages by page number. It is served by fetching around it and
// slicing rather than by refusing or by quietly returning the wrong rows.
func TestModelScope_ListModelsServesAnUnalignedOffset(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(300)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: 7, Limit: 5})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"owner/model-007", "owner/model-008", "owner/model-009",
		"owner/model-010", "owner/model-011",
	}, modelIDs(page))
	assert.Equal(t, modelScopeMaxPageSize, catalogue.requests[0].PageSize)
}

// A request that straddles a page boundary needs a second fetch, and the two
// have to be joined without losing or duplicating the row on the seam.
func TestModelScope_ListModelsSpansTwoPages(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(300)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: 98, Limit: 4})
	require.NoError(t, err)

	assert.Equal(t, []string{
		"owner/model-098", "owner/model-099", "owner/model-100", "owner/model-101",
	}, modelIDs(page))
	assert.Len(t, catalogue.requests, 2)
}

// Asking for more than the hub will serve in one page is not an error: the hub
// truncates silently at modelScopeMaxPageSize, so the provider has to keep
// asking rather than trust its own PageSize.
func TestModelScope_ListModelsAboveTheMaximumPageSize(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(300)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Limit: 250})
	require.NoError(t, err)

	assert.Len(t, page.Models, 250)
	assert.Equal(t, "owner/model-249", page.Models[249].Name)

	for _, request := range catalogue.requests {
		assert.LessOrEqual(t, request.PageSize, modelScopeMaxPageSize)
	}
}

// Running out of catalogue ends the walk. Without this the loop would keep
// asking the hub for pages past the end of a short search result.
func TestModelScope_ListModelsStopsAtTheEndOfTheCatalogue(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(3)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Limit: 250})
	require.NoError(t, err)

	assert.Len(t, page.Models, 3)
	assert.Len(t, catalogue.requests, 1)
}

// An offset past the end is an empty page, not an error — the same answer the
// private registries give.
func TestModelScope_ListModelsPastTheEndIsEmpty(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(10)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: 40, Limit: 5})
	require.NoError(t, err)
	assert.Empty(t, page.Models)
}

// Past the hub's deep-paging window the request is refused here, because the hub
// answers it with a 500 that is indistinguishable from an outage. The user is
// told the catalogue cannot be walked that far, not that ModelScope is down.
func TestModelScope_ListModelsRefusesADeepOffset(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(10), window: modelScopeMaxResultWindow}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: modelScopeMaxResultWindow, Limit: 20})

	assert.Nil(t, page)
	assert.ErrorIs(t, err, ErrNotSupported)
	assert.Empty(t, catalogue.requests, "the hub is not asked a question it answers with a 500")
}

// The last reachable page is short rather than absent: a request that starts
// inside the window and ends outside it is clipped, and — the part that actually
// breaks if the arithmetic is wrong — every page it fetches stays inside the
// window the hub enforces.
func TestModelScope_ListModelsClipsTheLastPageToTheWindow(t *testing.T) {
	catalogue := &modelScopeCatalogue{
		ids:    catalogueOf(modelScopeMaxResultWindow),
		window: modelScopeMaxResultWindow,
	}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Offset: modelScopeMaxResultWindow - 10, Limit: 30})
	require.NoError(t, err)

	assert.Len(t, page.Models, 10)

	for _, request := range catalogue.requests {
		assert.LessOrEqual(t, request.PageNumber*request.PageSize, modelScopeMaxResultWindow,
			"asked the hub for a window it refuses with a 500")
	}
}

// Every page size the provider can choose has to stay inside the window at the
// deepest offset it will accept. The failure this guards is silent in unit tests
// that only exercise round numbers: it appears as a 500 from the hub.
func TestModelScope_PageSizeNeverOverrunsTheWindow(t *testing.T) {
	for _, limit := range []int{1, 3, 7, 10, 25, 30, 50, 99, 100, 250} {
		for _, offset := range []int{0, 1, 7, 9000, 9960, 9970, 9990, 9999} {
			pageSize := modelScopePageSize(offset, limit)
			require.LessOrEqual(t, pageSize, modelScopeMaxPageSize)

			end := offset + limit
			if end > modelScopeMaxResultWindow {
				end = modelScopeMaxResultWindow
			}

			lastPage := (end-1)/pageSize + 1
			assert.LessOrEqual(t, lastPage*pageSize, modelScopeMaxResultWindow,
				"offset %d limit %d chose page size %d", offset, limit, pageSize)
		}
	}
}

// A caller that names no limit gets a page, not the whole catalogue: there are
// hundreds of thousands of entries.
func TestModelScope_ListModelsWithoutALimit(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(500), total: 237969}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{})
	require.NoError(t, err)

	assert.Len(t, page.Models, modelScopeDefaultLimit)
	require.NotNil(t, page.Total)
	assert.Equal(t, 237969, *page.Total)
}

// A model is addressed as "<owner>/<name>" everywhere else in the product, so
// that is the name a listing reports.
func TestModelScope_ListModelsReportsQualifiedNames(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: []string{"Qwen/Qwen3-8B"}}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Limit: 1})
	require.NoError(t, err)

	require.Len(t, page.Models, 1)
	assert.Equal(t, "Qwen/Qwen3-8B", page.Models[0].Name)
	require.Len(t, page.Models[0].Versions, 1)
	assert.Equal(t, v1.LatestVersion, page.Models[0].Versions[0].Name)
	assert.Equal(t, "2025-04-28T09:57:45Z", page.Models[0].Versions[0].CreationTime)
}

func TestModelScope_ListModelsPassesTheSearchTerm(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: catalogueOf(5)}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	_, err := ms.ListModels(ListOption{Search: "deepseek r1", Limit: 5})
	require.NoError(t, err)

	require.Len(t, catalogue.requests, 1)
	assert.Equal(t, "deepseek r1", catalogue.requests[0].Name)
}

// requestRecorder answers everything with one status and body and records the
// URLs asked for.
func requestRecorder(status int, body string) (*http.Client, *[]string) {
	requested := []string{}

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			requested = append(requested, req.URL.String())

			return jsonResponse(status, body), nil
		},
	}}

	return client, &requested
}

// The model card is the one part of a public model that can be read without
// downloading the checkpoint, so it is served rather than refused.
func TestModelScope_GetReadme(t *testing.T) {
	client, requested := requestRecorder(http.StatusOK, "---\nlicense: apache-2.0\n---\n# Qwen3\n")
	ms := &modelScope{url: modelScopeTestURL, client: client}

	readme, err := ms.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	require.NoError(t, err)

	// Verbatim markdown, front matter included: the server renders nothing.
	assert.Equal(t, "---\nlicense: apache-2.0\n---\n# Qwen3\n", readme.Content)
	assert.False(t, readme.Truncated)

	// No Revision at all for the default version. The hub resolves an absent
	// Revision to the repository's own default branch, and its repositories do
	// not agree on what that is called — Qwen/Qwen3-8B is on "master", and asking
	// it for "main" is a 404.
	require.Len(t, *requested, 1)
	assert.Equal(t, modelScopeTestURL+"/api/v1/models/Qwen/Qwen3-8B/repo?FilePath=README.md", (*requested)[0])
}

func TestModelScope_GetReadmeUsesTheRequestedRevision(t *testing.T) {
	client, requested := requestRecorder(http.StatusOK, "# Qwen3\n")
	ms := &modelScope{url: "https://ms-mirror.example", client: client}

	_, err := ms.GetReadme("Qwen/Qwen3-8B", "v1.0.0")
	require.NoError(t, err)

	assert.Equal(t,
		"https://ms-mirror.example/api/v1/models/Qwen/Qwen3-8B/repo?FilePath=README.md&Revision=v1.0.0",
		(*requested)[0])
}

// "there is no model card" has to be distinguishable from "the hub could not be
// asked", or a caller cannot tell the user which one happened.
func TestModelScope_GetReadmeMissingIsNotFound(t *testing.T) {
	client, _ := requestRecorder(http.StatusNotFound,
		`{"Code":10990101007,"Message":"获取模型文件失败，文件内容为空","Success":false}`)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	_, err := ms.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	assert.ErrorIs(t, err, ErrNotFound)
}

func TestModelScope_GetReadmeIsCapped(t *testing.T) {
	client, _ := requestRecorder(http.StatusOK, string(bytes.Repeat([]byte("a"), MaxReadmeBytes+4096)))
	ms := &modelScope{url: modelScopeTestURL, client: client}

	readme, err := ms.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	require.NoError(t, err)
	assert.True(t, readme.Truncated)
	assert.Len(t, readme.Content, MaxReadmeBytes)
}

const modelScopeTestConfig = `{
	"architectures": ["Qwen3ForCausalLM"],
	"num_hidden_layers": 36,
	"num_attention_heads": 32,
	"num_key_value_heads": 8,
	"head_dim": 128,
	"max_position_embeddings": 40960,
	"torch_dtype": "bfloat16"
}`

// The shape a model reports must not depend on which registry it came from: the
// same config.json parser runs for ModelScope, Hugging Face and the private path.
func TestModelScope_GetModelDetail(t *testing.T) {
	client, requested := requestRecorder(http.StatusOK, modelScopeTestConfig)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	version, err := ms.GetModelDetail("Qwen/Qwen3-8B", v1.LatestVersion)
	require.NoError(t, err)

	assert.Equal(t, v1.LatestVersion, version.Name)
	assert.Equal(t, "Qwen3ForCausalLM", version.Info.Architecture)
	require.NotNil(t, version.Info.NumHiddenLayers)
	assert.Equal(t, 36, *version.Info.NumHiddenLayers)
	require.NotNil(t, version.Info.NumKeyValueHeads)
	assert.Equal(t, 8, *version.Info.NumKeyValueHeads)
	require.NotNil(t, version.Info.HeadDim)
	assert.Equal(t, 128, *version.Info.HeadDim)

	// The weights are never downloaded: config.json is the only file read.
	require.Len(t, *requested, 1)
	assert.Contains(t, (*requested)[0], "FilePath=config.json")
}

// ModelScope publishes no equivalent of the Hub's safetensors tally, and its
// StorageSize is bytes on disk rather than a parameter count. Saying so is the
// point — the acceptance criterion is that what cannot be read is reported
// missing rather than guessed.
func TestModelScope_GetModelDetailReportsAnUnknownParameterCount(t *testing.T) {
	client, _ := requestRecorder(http.StatusOK, modelScopeTestConfig)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	version, err := ms.GetModelDetail("Qwen/Qwen3-8B", v1.LatestVersion)
	require.NoError(t, err)

	assert.Empty(t, version.Info.ParameterCount)
	assert.Contains(t, version.Info.MissingFields, v1.ModelInfoFieldParameterCount)
	assert.Equal(t, 1, countOccurrences(version.Info.MissingFields, v1.ModelInfoFieldParameterCount),
		"parameter_count is listed once, not once per code path that noticed it")
}

func countOccurrences(values []string, want string) int {
	count := 0

	for _, value := range values {
		if value == want {
			count++
		}
	}

	return count
}

// A repository with no config.json is a real answer, not a failure: plenty of
// things on the hub are not transformers checkpoints.
func TestModelScope_GetModelDetailWithoutConfig(t *testing.T) {
	client, _ := requestRecorder(http.StatusNotFound,
		`{"Code":10990101007,"Message":"获取模型文件失败，文件内容为空","Success":false}`)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	version, err := ms.GetModelDetail("owner/not-a-checkpoint", v1.LatestVersion)
	require.NoError(t, err)

	assert.Empty(t, version.Info.Architecture)
	assert.Contains(t, version.Info.MissingFields, v1.ModelInfoFieldArchitecture)
	assert.Contains(t, version.Info.MissingFields, v1.ModelInfoFieldParameterCount)
}

// A refusal is not a missing checkpoint, and the difference decides whether the
// user is asked for a token or told the model has no config.
func TestModelScope_GetModelDetailRefusalIsNotNotFound(t *testing.T) {
	client, _ := requestRecorder(http.StatusForbidden, `{"Code":10010205001,"Message":"forbidden"}`)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	_, err := ms.GetModelDetail("owner/gated", v1.LatestVersion)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized)
	assert.NotErrorIs(t, err, ErrNotFound)
}

// The three failures the ticket asks a client to be able to tell apart, each
// arriving as its own type rather than as a message to match on.
func TestModelScope_ErrorsAreDistinguishable(t *testing.T) {
	tests := []struct {
		name    string
		client  *http.Client
		wantIs  error
		wantNot []error
	}{
		{
			name: "unreachable",
			client: &http.Client{Transport: &MockRoundTripper{
				RoundTripFunc: func(req *http.Request) (*http.Response, error) {
					return nil, fmt.Errorf("dial tcp: connect: network is unreachable")
				},
			}},
			wantNot: []error{ErrUnauthorized, ErrRateLimited, ErrNotFound, ErrNotSupported},
		},
		{
			name: "credentials rejected",
			client: func() *http.Client {
				client, _ := requestRecorder(http.StatusUnauthorized, `{"Code":10010103009,"Message":"token"}`)

				return client
			}(),
			wantIs:  ErrUnauthorized,
			wantNot: []error{ErrRateLimited, ErrNotFound},
		},
		{
			name: "throttled",
			client: func() *http.Client {
				client, _ := requestRecorder(http.StatusTooManyRequests, `{"Code":429,"Message":"slow down"}`)

				return client
			}(),
			wantIs:  ErrRateLimited,
			wantNot: []error{ErrUnauthorized, ErrNotFound},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			ms := &modelScope{url: modelScopeTestURL, client: tt.client}

			_, err := ms.ListModels(ListOption{Limit: 1})
			require.Error(t, err)

			if tt.wantIs != nil {
				assert.ErrorIs(t, err, tt.wantIs)
			}

			for _, unwanted := range tt.wantNot {
				assert.NotErrorIs(t, err, unwanted)
			}
		})
	}
}

// A throttled request is retried before it is reported, and a retried request
// carries its body again — a search is a PUT, so a body that was consumed on the
// first attempt would make the retry ask a different question.
func TestModelScope_RateLimitRecoversOnRetry(t *testing.T) {
	var bodies []string

	attempt := 0
	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			body, err := io.ReadAll(req.Body)
			if err != nil {
				return nil, err
			}

			bodies = append(bodies, string(body))
			attempt++

			if attempt == 1 {
				return jsonResponse(http.StatusTooManyRequests, `{"Code":429}`), nil
			}

			return jsonResponse(http.StatusOK,
				`{"Code":200,"Success":true,"Data":{"Model":{"Models":[{"Path":"owner","Name":"m"}],"TotalCount":1}}}`), nil
		},
	}}

	ms := &modelScope{url: modelScopeTestURL, client: client}

	page, err := ms.ListModels(ListOption{Search: "qwen", Limit: 1})
	require.NoError(t, err)
	assert.Len(t, page.Models, 1)

	require.Len(t, bodies, 2)
	assert.Equal(t, bodies[0], bodies[1], "the retry must ask the same question as the first attempt")
	assert.Contains(t, bodies[1], `"Name":"qwen"`)
}

// The catalogue and file endpoints answer a request carrying a rejected token
// exactly as they answer an anonymous one, so a bad credential is only ever
// visible on the login endpoint. Without this check a registry configured with a
// dead token would report itself healthy.
func TestModelScope_HealthyCheckValidatesTheToken(t *testing.T) {
	var paths []string

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			paths = append(paths, req.URL.Path)

			if req.URL.Path == modelScopeLoginPath {
				// What the hub really answers a bad token: 400, not 401.
				return jsonResponse(http.StatusBadRequest,
					`{"Code":10010103009,"Message":"AccessToken error","Success":false}`), nil
			}

			return jsonResponse(http.StatusOK,
				`{"Code":200,"Success":true,"Data":{"Model":{"Models":[],"TotalCount":0}}}`), nil
		},
	}}

	ms := &modelScope{url: modelScopeTestURL, client: client, apiToken: "ms-dead-token"}

	err := ms.HealthyCheck()
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrUnauthorized,
		"a 400 from the login endpoint is a credential problem, not a malformed request")
	assert.Equal(t, []string{modelScopeLoginPath}, paths,
		"the catalogue is not consulted once the token is known to be bad")
}

func TestModelScope_HealthyCheckWithAGoodToken(t *testing.T) {
	var authorization []string

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			authorization = append(authorization, req.Header.Get("Authorization"))

			if req.URL.Path == modelScopeLoginPath {
				return jsonResponse(http.StatusOK, `{"Code":200,"Success":true,"Data":{}}`), nil
			}

			return jsonResponse(http.StatusOK,
				`{"Code":200,"Success":true,"Data":{"Model":{"Models":[{"Path":"a","Name":"b"}],"TotalCount":1}}}`), nil
		},
	}}

	ms := &modelScope{url: modelScopeTestURL, client: client, apiToken: "ms-good-token"}

	require.NoError(t, ms.HealthyCheck())
	require.Len(t, authorization, 2)
	// The catalogue request carries the credential too, so a token that grants
	// extra visibility is actually used.
	assert.Equal(t, "Bearer ms-good-token", authorization[1])
}

// Without a token there is nothing to validate, so the reachability check is the
// catalogue request alone.
func TestModelScope_HealthyCheckWithoutAToken(t *testing.T) {
	var paths []string

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			paths = append(paths, req.URL.Path)
			assert.Empty(t, req.Header.Get("Authorization"))

			return jsonResponse(http.StatusOK,
				`{"Code":200,"Success":true,"Data":{"Model":{"Models":[],"TotalCount":0}}}`), nil
		},
	}}

	ms := &modelScope{url: modelScopeTestURL, client: client}

	require.NoError(t, ms.Connect())
	assert.Equal(t, []string{modelScopeSearchPath}, paths)
}

func TestModelScope_HealthyCheckReportsAnUnreachableHub(t *testing.T) {
	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			return nil, fmt.Errorf("dial tcp: i/o timeout")
		},
	}}

	ms := &modelScope{url: modelScopeTestURL, client: client}

	err := ms.Connect()
	require.Error(t, err)
	assert.Contains(t, err.Error(), "i/o timeout")
	assert.NotErrorIs(t, err, ErrUnauthorized)
}

// A 200 whose envelope says the call failed is still a failure. The hub is
// consistent about the status today; the envelope is what it treats as
// authoritative.
func TestModelScope_UnsuccessfulEnvelopeIsAnError(t *testing.T) {
	client, _ := requestRecorder(http.StatusOK,
		`{"Code":10010207001,"Message":"list_models_failed","Success":false}`)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	_, err := ms.ListModels(ListOption{Limit: 1})
	require.Error(t, err)
	assert.Contains(t, err.Error(), "list_models_failed")
}

// A public catalogue is read-only. Each refusal has to carry ErrNotSupported so
// the API answers "this registry cannot do that" rather than a server error.
func TestModelScope_UnsupportedOperationsAreTyped(t *testing.T) {
	ms := &modelScope{url: modelScopeTestURL}

	_, err := ms.GetModelVersion("m", "v")
	assert.ErrorIs(t, err, ErrNotSupported)

	assert.ErrorIs(t, ms.DeleteModel("m", "v"), ErrNotSupported)
	assert.ErrorIs(t, ms.ImportModel(strings.NewReader(""), "m", "v", io.Discard), ErrNotSupported)
	assert.ErrorIs(t, ms.ExportModel("m", "v", "/tmp"), ErrNotSupported)
	assert.ErrorIs(t, ms.SetManualModelInfo("m", "v", &v1.ModelInfo{}), ErrNotSupported)

	_, err = ms.GetModelPath("m", "v")
	assert.ErrorIs(t, err, ErrNotSupported)

	// Storage figures are refused rather than reported as zero, which is what
	// makes the list show "-" instead of "0 B" for a hub.
	_, err = ms.CollectUsage()
	assert.ErrorIs(t, err, ErrNotSupported)

	version, err := ms.GetNFSVersion()
	assert.NoError(t, err)
	assert.Empty(t, version)
}

// A version read from a listing is what a client puts into "model get
// <name>:<version>" and into a detail URL, so a detail that answered with the
// hub's own branch name would name a version the listing never shows. Both
// providers go through reportedVersion for exactly this reason; this pins that
// ModelScope did not grow a second rule.
func TestModelScope_DetailNamesTheVersionTheListingEmits(t *testing.T) {
	catalogue := &modelScopeCatalogue{ids: []string{"Qwen/Qwen3-8B"}}
	ms := &modelScope{url: modelScopeTestURL, client: catalogue.client()}

	page, err := ms.ListModels(ListOption{Limit: 1})
	require.NoError(t, err)
	require.Len(t, page.Models[0].Versions, 1)

	listed := page.Models[0].Versions[0].Name
	assert.Equal(t, v1.LatestVersion, listed)

	client, _ := requestRecorder(http.StatusOK, modelScopeTestConfig)
	ms.client = client

	detail, err := ms.GetModelDetail("Qwen/Qwen3-8B", listed)
	require.NoError(t, err)
	assert.Equal(t, listed, detail.Name,
		"a detail must answer under the version name the listing emitted")
}

// The shared rule takes the registry's own default-branch name, and getting that
// name from the provider is the whole point: "master" is ModelScope's default and
// maps back to "latest", while "main" is a branch ModelScope repositories do not
// have — reporting it as "latest" would both lose the caller's request and name a
// version that repository cannot resolve.
func TestReportedVersionUsesTheProvidersDefaultBranch(t *testing.T) {
	tests := []struct {
		name          string
		version       string
		defaultBranch string
		want          string
	}{
		{"hub default branch", defaultRevision, defaultRevision, v1.LatestVersion},
		{"modelscope default branch", modelScopeDefaultRevision, modelScopeDefaultRevision, v1.LatestVersion},
		{"empty is the default branch", "", modelScopeDefaultRevision, v1.LatestVersion},
		{"latest stays latest", v1.LatestVersion, modelScopeDefaultRevision, v1.LatestVersion},
		// The regression this parameter exists for.
		{"main is a real branch elsewhere", defaultRevision, modelScopeDefaultRevision, defaultRevision},
		{"master is a real branch on the hub", modelScopeDefaultRevision, defaultRevision, modelScopeDefaultRevision},
		{"a tag is itself", "v1.0.0", modelScopeDefaultRevision, "v1.0.0"},
		{"no single default name", "master", "", "master"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			assert.Equal(t, tt.want, reportedVersion(tt.version, tt.defaultBranch))
		})
	}
}

// The measured fact behind modelScopeDefaultRevision, asserted where it is used:
// a caller naming ModelScope's default branch gets "latest" back, and one naming
// "main" gets "main" — because ModelScope repositories do not have a "main".
func TestModelScope_DetailNamesTheDefaultBranchLatest(t *testing.T) {
	client, _ := requestRecorder(http.StatusOK, modelScopeTestConfig)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	detail, err := ms.GetModelDetail("Qwen/Qwen3-8B", "master")
	require.NoError(t, err)
	assert.Equal(t, v1.LatestVersion, detail.Name)

	detail, err = ms.GetModelDetail("Qwen/Qwen3-8B", "main")
	require.NoError(t, err)
	assert.Equal(t, "main", detail.Name, "main is not ModelScope's default branch and must not be renamed")
}

// An explicit revision is reported back as itself: it is a real name on the hub
// and a client that asked for it must be able to ask for it again.
func TestModelScope_DetailKeepsAnExplicitRevision(t *testing.T) {
	client, requested := requestRecorder(http.StatusOK, modelScopeTestConfig)
	ms := &modelScope{url: modelScopeTestURL, client: client}

	detail, err := ms.GetModelDetail("Qwen/Qwen3-8B", "v1.0.0")
	require.NoError(t, err)

	assert.Equal(t, "v1.0.0", detail.Name)
	assert.Contains(t, (*requested)[0], "Revision=v1.0.0")
}

// The visibility rule is what turns off the write controls, the storage figures
// and the read-through cache. A kind that is public in the provider but private
// here would get all three wrong, silently.
func TestModelScope_IsPublic(t *testing.T) {
	assert.Equal(t, v1.ModelRegistryVisibilityPublic,
		v1.VisibilityForModelRegistryType(v1.ModelScopeModelRegistryType))
}

// modelScopeRepo answers the two file endpoints the way the hub does, so a test
// can exercise the disagreement between them: the file endpoint 404s on an
// unknown revision, the tree endpoint answers 200 with a null list.
type modelScopeRepo struct {
	// revisions maps a revision to the files it holds. A revision absent from the
	// map is one the repository does not have. The empty string is the default.
	revisions map[string][]map[string]interface{}
	// files maps "<revision>/<path>" to its bytes.
	files map[string]string
	// requested records every URL asked for.
	requested []string
}

func (r *modelScopeRepo) client() *http.Client {
	return &http.Client{Transport: &MockRoundTripper{RoundTripFunc: func(req *http.Request) (*http.Response, error) {
		r.requested = append(r.requested, req.URL.String())

		revision := req.URL.Query().Get("Revision")
		files, known := r.revisions[revision]

		if strings.HasSuffix(req.URL.Path, modelScopeRepoFilesPath) {
			// The trap: an unknown revision is not an error here. It is a success
			// whose file list happens to be null.
			payload := map[string]interface{}{
				"Code": 200, "Message": "success", "Success": true,
				"Data": map[string]interface{}{"Files": nil},
			}
			if known {
				payload["Data"] = map[string]interface{}{"Files": files}
			}

			body, err := json.Marshal(payload)
			if err != nil {
				return nil, err
			}

			return jsonResponse(http.StatusOK, string(body)), nil
		}

		content, ok := r.files[revision+"/"+req.URL.Query().Get("FilePath")]
		if !ok {
			// Missing file and unknown revision are the same answer here.
			return jsonResponse(http.StatusNotFound,
				`{"Code":10990101007,"Message":"获取模型文件失败，文件内容为空","Success":false}`), nil
		}

		return jsonResponse(http.StatusOK, content), nil
	}}}
}

// A repository whose default revision holds config.json and README.md.
func populatedRepo() *modelScopeRepo {
	return &modelScopeRepo{
		revisions: map[string][]map[string]interface{}{
			"": {
				{"Name": "config.json", "Path": "config.json", "Size": 659, "Type": "blob", "IsLFS": false},
				{"Name": "model.safetensors", "Path": "model.safetensors", "Size": 988097824, "Type": "blob", "IsLFS": true},
			},
			"v1.0.0": {
				{"Name": "config.json", "Path": "config.json", "Size": 659, "Type": "blob", "IsLFS": false},
			},
		},
		files: map[string]string{
			"/config.json":       modelScopeTestConfig,
			"/README.md":         "# a model\n",
			"v1.0.0/config.json": modelScopeTestConfig,
		},
	}
}

// The trap this exists for: ModelScope answers an unknown revision with HTTP 200,
// Code 200, Success true and a *null* file list — measured on
// Qwen/Qwen2.5-0.5B-Instruct, where Revision=main and Revision=zzz-not-real are
// indistinguishable from each other. Reading that as "no files" would show a
// user an empty repository for what is a typo.
func TestModelScope_ListRepoFilesRejectsANullFileList(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	files, err := ms.listRepoFiles("Qwen/Qwen2.5-0.5B-Instruct", "zzz-not-real")

	assert.Nil(t, files)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound, "a null file list is not an empty repository")
	assert.Contains(t, err.Error(), "zzz-not-real")
}

// The same applies to "main", which is the Hub's default branch name and not
// ModelScope's. It is not a differently-spelled default; it is nothing.
func TestModelScope_ListRepoFilesRejectsTheHubsDefaultBranchName(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	_, err := ms.listRepoFiles("Qwen/Qwen2.5-0.5B-Instruct", defaultRevision)
	assert.ErrorIs(t, err, ErrNotFound)
}

// An empty list is a real answer and must stay distinguishable from a null one:
// a repository can legitimately hold nothing.
func TestModelScope_ListRepoFilesAllowsAnEmptyRepository(t *testing.T) {
	repo := populatedRepo()
	repo.revisions["empty-branch"] = []map[string]interface{}{}
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	files, err := ms.listRepoFiles("owner/empty", "empty-branch")
	require.NoError(t, err)
	assert.Empty(t, files)
}

// What NEU-689 reads: a path to fetch by, a size to budget with, and the LFS
// flag that marks the weights.
func TestModelScope_ListRepoFilesCarriesWhatADownloaderNeeds(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	files, err := ms.listRepoFiles("Qwen/Qwen2.5-0.5B-Instruct", v1.LatestVersion)
	require.NoError(t, err)
	require.Len(t, files, 2)

	assert.Equal(t, "config.json", files[0].Path)
	assert.Equal(t, int64(659), files[0].Size)
	assert.False(t, files[0].IsLFS)
	assert.Equal(t, "model.safetensors", files[1].Path)
	assert.Equal(t, int64(988097824), files[1].Size)
	assert.True(t, files[1].IsLFS, "the weights are LFS-stored and a downloader has to know")

	// The default revision is addressed by omitting Revision, never by guessing a
	// branch name.
	require.Len(t, repo.requested, 1)
	assert.NotContains(t, repo.requested[0], "Revision=")
	assert.Contains(t, repo.requested[0], "/repo/files?")
}

// A mistyped revision must not be rendered as a real checkpoint that happens to
// say nothing about itself. The file endpoint cannot tell the two apart, so the
// tree is consulted on the miss.
func TestModelScope_GetModelDetailRejectsAnUnknownRevision(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	version, err := ms.GetModelDetail("Qwen/Qwen2.5-0.5B-Instruct", "zzz-not-real")

	assert.Nil(t, version)
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "no revision", "the reader has to be told which of the two it was")
}

// The other half: a repository that really has no config.json still answers, with
// every field missing. Losing this would trade one silent wrong answer for
// another.
func TestModelScope_GetModelDetailStillAnswersWithoutConfigOnAKnownRevision(t *testing.T) {
	repo := populatedRepo()
	repo.revisions["no-config"] = []map[string]interface{}{
		{"Name": "README.md", "Path": "README.md", "Size": 10, "Type": "blob", "IsLFS": false},
	}
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	version, err := ms.GetModelDetail("owner/not-a-checkpoint", "no-config")
	require.NoError(t, err)

	assert.Equal(t, "no-config", version.Name)
	assert.Contains(t, version.Info.MissingFields, v1.ModelInfoFieldArchitecture)
}

// A README miss gets the same treatment: "this model has no README" is the wrong
// thing to tell someone who mistyped a branch.
func TestModelScope_GetReadmeRejectsAnUnknownRevision(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	_, err := ms.GetReadme("Qwen/Qwen2.5-0.5B-Instruct", "zzz-not-real")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "no revision")

	// And a revision that does exist but has no README still says so.
	_, err = ms.GetReadme("Qwen/Qwen2.5-0.5B-Instruct", "v1.0.0")
	require.Error(t, err)
	assert.ErrorIs(t, err, ErrNotFound)
	assert.Contains(t, err.Error(), "has no README.md")
}

// The extra tree lookup happens only on a miss. A detail that found its
// config.json costs exactly one request, as it did before this check existed.
func TestModelScope_GetModelDetailCostsOneRequestOnTheHappyPath(t *testing.T) {
	repo := populatedRepo()
	ms := &modelScope{url: modelScopeTestURL, client: repo.client()}

	_, err := ms.GetModelDetail("Qwen/Qwen2.5-0.5B-Instruct", v1.LatestVersion)
	require.NoError(t, err)
	assert.Len(t, repo.requested, 1)
}
