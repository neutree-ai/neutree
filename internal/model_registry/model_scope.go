package model_registry

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/pkg/errors"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/modelmeta"
)

const (
	// modelScopeSearchPath is the catalogue search. It answers to PUT, not POST or
	// GET; the other verbs are not routed.
	modelScopeSearchPath = "/api/v1/dolphin/models"
	// modelScopeModelPath prefixes the per-repository endpoints, which are
	// addressed as <prefix>/<owner>/<name>.
	modelScopeModelPath = "/api/v1/models"
	// modelScopeRepoFilePath fetches one file out of a repository, appended to a
	// model's path: <modelScopeModelPath>/<owner>/<name>/repo?Revision=&FilePath=
	modelScopeRepoFilePath = "/repo"
	// modelScopeRepoFilesPath lists a revision's files, appended the same way and
	// taking a Root= parameter for the subtree.
	modelScopeRepoFilesPath = "/repo/files"
	// modelScopeLoginPath evaluates an access token. Nothing else does: the read
	// endpoints below answer a request carrying a bogus token exactly as they
	// answer an anonymous one.
	modelScopeLoginPath = "/api/v1/login"

	// modelScopeMaxPageSize is the largest page the hub will serve. Asking for
	// more is not refused, it is silently truncated to this, so a client that
	// trusted its own PageSize would quietly skip rows.
	modelScopeMaxPageSize = 100
	// modelScopeMaxResultWindow bounds how deep the catalogue can be paged:
	// offset + page size may not exceed it. Past that the hub answers HTTP 500,
	// which would otherwise be reported to the user as "the registry is down".
	modelScopeMaxResultWindow = 10000
	// modelScopeDefaultLimit is the page served when a caller names no limit. The
	// catalogue has hundreds of thousands of entries, so "all of them" is not an
	// option the way it is for a private registry.
	modelScopeDefaultLimit = modelScopeMaxPageSize

	// modelScopeDefaultRevision is what ModelScope calls a repository's default
	// branch. Surveyed across seven repositories of different kinds — Qwen,
	// DeepSeek, Llama, Wan, iic, AI-ModelScope, moonshotai — every one reports
	// Revision "master" and lists "master" as its only non-PR branch.
	//
	// It is emphatically not "main", which is the Hub's name. On
	// Qwen/Qwen2.5-0.5B-Instruct, Revision=main behaves exactly like a revision
	// that was never created: the file endpoint 404s and the tree endpoint
	// answers 200 with a null file list. Hard-coding the Hub's name here would
	// therefore point at nothing, not merely at something differently named.
	//
	// This names a version back to a caller; it does not address one. The wire
	// never carries it — see revision — so a repository that somehow used another
	// default is still read correctly; that branch would only be reported under
	// its own name rather than as "latest".
	modelScopeDefaultRevision = "master"
)

// Where a ModelScope model's files come from, for whoever wires the download
// path (NEU-689). All three are on the /api/v1 REST API; the /api/... routes
// without the version segment are the website's SPA and answer HTML.
//
//	list a revision's files  GET <url>/api/v1/models/<owner>/<name>/repo/files?Revision=<rev>&Root=<subtree>
//	fetch one file           GET <url>/api/v1/models/<owner>/<name>/repo?Revision=<rev>&FilePath=<path>
//	repository metadata      GET <url>/api/v1/models/<owner>/<name>
//
// The pieces to reuse rather than re-derive: <owner>/<name> is
// v1.GeneralModel.Name exactly as a listing emits it; <rev> is what revision()
// returns, and it must go through revision() because "" means "the repository's
// own default" and is the only safe way to spell that (see
// modelScopeDefaultRevision); listRepoFiles already carries the null-file-list
// trap below, so use it rather than decoding the tree again.
//
// Files are listed with a Size and an IsLFS flag, so a downloader can budget
// before fetching. The registry stays read-only: nothing here writes.

// errModelScopeNotSupported wraps ErrNotSupported for the operations a public
// catalogue has no answer for at all. ModelScope is read-only here.
var errModelScopeNotSupported = errors.Wrap(ErrNotSupported, "operation not supported for ModelScope registry")

type modelScope struct {
	apiToken string
	client   *http.Client
	url      string
}

func newModelScope(registry *v1.ModelRegistry) (*modelScope, error) {
	if registry.Spec.Url == "" {
		return nil, errors.New("registry.Spec.Url cannot be empty")
	}

	parsedUrl, err := url.Parse(registry.Spec.Url)
	if err != nil {
		return nil, errors.Wrap(err, "invalid registry.Spec.Url")
	}

	if parsedUrl.Host == "" || parsedUrl.Scheme == "" {
		return nil, errors.New("invalid registry.Spec.Url")
	}

	return &modelScope{
		url:      strings.TrimSuffix(parsedUrl.String(), "/"),
		apiToken: registry.Spec.Credentials,
		client:   sharedClient,
	}, nil
}

func (ms *modelScope) Connect() error {
	return ms.healthyCheck()
}

func (ms *modelScope) Disconnect() error {
	return nil
}

// modelScopeEnvelope is the shape every ModelScope endpoint answers in. Code and
// Message carry the hub's own classification, which is finer-grained than the
// HTTP status and is what appears in an error the user reads.
type modelScopeEnvelope struct {
	Code    int64           `json:"Code"`
	Message string          `json:"Message"`
	Success bool            `json:"Success"`
	Data    json.RawMessage `json:"Data"`
}

// modelScopeModel is the subset of a catalogue entry we use. The hub returns
// around eighty fields per model; naming the four we read keeps the decode from
// silently depending on the rest.
type modelScopeModel struct {
	// Name is the repository name and Path its owner; the model is addressed as
	// "<Path>/<Name>" everywhere else, including in a deploy request.
	Name string `json:"Name"`
	Path string `json:"Path"`
	// CreatedTime is Unix seconds.
	CreatedTime int64 `json:"CreatedTime"`
}

func (m modelScopeModel) id() string {
	if m.Path == "" {
		return m.Name
	}

	return m.Path + "/" + m.Name
}

type modelScopeSearchData struct {
	Model struct {
		Models     []modelScopeModel `json:"Models"`
		TotalCount int               `json:"TotalCount"`
	} `json:"Model"`
}

// ListModels serves one page of the catalogue.
//
// Unlike the Hugging Face Hub, ModelScope pages by row offset and reports how
// many models matched, so both are passed through as they are: Offset is honoured
// and Total is the hub's own count. Verified against the live API — a page of ten
// is exactly two consecutive pages of five, in the same order.
//
// The one thing it cannot do is page arbitrarily deep: offset plus page size may
// not exceed modelScopeMaxResultWindow, and past that the hub answers HTTP 500.
// That is refused here as ErrNotSupported, so the user is told the catalogue
// cannot be walked that far rather than that it is unreachable.
func (ms *modelScope) ListModels(option ListOption) (*ModelPage, error) {
	limit := option.Limit
	if limit <= 0 {
		limit = modelScopeDefaultLimit
	}

	offset := option.Offset
	if offset >= modelScopeMaxResultWindow {
		return nil, errors.Wrapf(ErrNotSupported,
			"the ModelScope API cannot list models past offset %d; narrow the search instead",
			modelScopeMaxResultWindow)
	}

	// The requested rows, clipped to the window. Clipping rather than refusing
	// means the last reachable page is short instead of absent.
	end := offset + limit
	if end > modelScopeMaxResultWindow {
		end = modelScopeMaxResultWindow
	}

	pageSize := modelScopePageSize(offset, limit)
	firstPage := offset/pageSize + 1
	// How many rows of the first fetched page fall before the requested offset.
	skip := offset - (firstPage-1)*pageSize
	want := end - offset

	var (
		collected []modelScopeModel
		total     int
	)

	for page := firstPage; len(collected) < skip+want; page++ {
		batch, matched, err := ms.searchModels(option.Search, pageSize, page)
		if err != nil {
			return nil, err
		}

		collected = append(collected, batch...)
		total = matched

		// A short page is the end of the catalogue for this search.
		if len(batch) < pageSize {
			break
		}
	}

	if skip > len(collected) {
		skip = len(collected)
	}

	collected = collected[skip:]
	if len(collected) > want {
		collected = collected[:want]
	}

	result := make([]v1.GeneralModel, 0, len(collected))

	for i := range collected {
		result = append(result, v1.GeneralModel{
			Name: collected[i].id(),
			Versions: []v1.ModelVersion{
				{
					Name:         v1.LatestVersion,
					CreationTime: time.Unix(collected[i].CreatedTime, 0).UTC().Format(time.RFC3339Nano),
				},
			},
		})
	}

	return &ModelPage{Models: result, Total: KnownTotal(total)}, nil
}

// modelScopePageSize picks the page size to ask the hub for.
//
// The hub pages by page number, so a requested offset is only directly
// expressible when it is a whole number of pages. When it is — the case every
// paging client produces — the page is fetched exactly. Otherwise the request is
// served by fetching maximum-size pages around it and slicing, which costs at
// most one extra round trip.
//
// modelScopeMaxPageSize is also the fallback because it divides
// modelScopeMaxResultWindow: that is what keeps the last reachable page from
// asking for a window the hub refuses.
func modelScopePageSize(offset, limit int) int {
	if limit <= modelScopeMaxPageSize && offset%limit == 0 && offset+limit <= modelScopeMaxResultWindow {
		return limit
	}

	return modelScopeMaxPageSize
}

// searchModels asks for one page of the catalogue. Page numbers are 1-based.
func (ms *modelScope) searchModels(search string, pageSize, pageNumber int) ([]modelScopeModel, int, error) {
	body, err := json.Marshal(map[string]interface{}{
		"PageSize":   pageSize,
		"PageNumber": pageNumber,
		"SortBy":     "Default",
		"Target":     "",
		// The hub requires the key even when no facet filter is applied.
		"SingleCriterion": []interface{}{},
		"Name":            search,
	})
	if err != nil {
		return nil, 0, err
	}

	// PUT, not POST: the other verbs are not routed on this path.
	req, err := http.NewRequest(http.MethodPut, ms.url+modelScopeSearchPath, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}

	req.Header.Set("Content-Type", "application/json")

	raw, err := ms.do(req, "failed to list models")
	if err != nil {
		return nil, 0, err
	}

	var data modelScopeSearchData
	if err := json.Unmarshal(raw, &data); err != nil {
		return nil, 0, errors.Wrap(err, "failed to list models")
	}

	return data.Model.Models, data.Model.TotalCount, nil
}

// HealthyCheck checks the health of the ModelScope API.
func (ms *modelScope) HealthyCheck() error {
	return ms.healthyCheck()
}

func (ms *modelScope) healthyCheck() error {
	if ms.apiToken != "" {
		if err := ms.validateToken(); err != nil {
			return errors.Wrap(err, "invalid ModelScope access token")
		}
	}

	if _, _, err := ms.searchModels("", 1, 1); err != nil {
		return errors.Wrap(err, "failed to list models from ModelScope API")
	}

	return nil
}

// validateToken is the only way a bad credential surfaces at all: the catalogue
// and file endpoints answer a request carrying a rejected token exactly as they
// answer an anonymous one, so without this check a registry configured with a
// dead token would report itself healthy and then quietly fail to see anything
// private.
//
// Any refusal here is a credential problem — the hub answers a bad token with
// 400, not 401 — so the whole call is mapped to ErrUnauthorized rather than
// going through responseError.
func (ms *modelScope) validateToken() error {
	body, err := json.Marshal(map[string]string{"AccessToken": ms.apiToken})
	if err != nil {
		return err
	}

	req, err := http.NewRequest(http.MethodPost, ms.url+modelScopeLoginPath, bytes.NewReader(body))
	if err != nil {
		return err
	}

	req.Header.Set("Content-Type", "application/json")

	resp, err := ms.doWithRetry(req)
	if err != nil {
		return errors.Wrap(err, "failed to validate ModelScope access token")
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusTooManyRequests {
		return errors.Wrapf(ErrRateLimited, "failed to validate ModelScope access token: %s", resp.Status)
	}

	if resp.StatusCode != http.StatusOK {
		detail := modelScopeErrorDetail(resp)

		return errors.Wrapf(ErrUnauthorized,
			"the configured access token was rejected (%s)%s", resp.Status, detail)
	}

	return nil
}

// Implement the remaining ModelRegistry interface methods with "not supported" errors

func (ms *modelScope) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	return nil, errModelScopeNotSupported
}

// ModelScopeRepoFile is one entry of a repository revision's file tree. Size and
// IsLFS are what let a downloader decide what to fetch and what it will cost
// before fetching any of it.
type ModelScopeRepoFile struct {
	// Path is the file's path within the repository, which is what the file
	// endpoint's FilePath parameter takes.
	Path string `json:"Path"`
	Name string `json:"Name"`
	// Size is the file's size in bytes.
	Size int64 `json:"Size"`
	// Type is "blob" for a file and "tree" for a directory.
	Type string `json:"Type"`
	// IsLFS marks a file stored via LFS, which is how the weights are stored.
	IsLFS bool `json:"IsLFS"`
}

// modelScopeRepoTree is the tree endpoint's payload. Files is a pointer so that
// an absent or null list stays distinguishable from an empty one — that
// difference is the whole point of listRepoFiles.
type modelScopeRepoTree struct {
	Files *[]ModelScopeRepoFile `json:"Files"`
}

// listRepoFiles lists the files of one revision of a repository.
//
// It exists as much for the trap it closes as for the listing. Asked for a
// revision that does not exist, ModelScope does not answer with an error: it
// answers HTTP 200 with `Code: 200`, `Success: true`, `Message: "success"` and a
// **null** file list — measured on Qwen/Qwen2.5-0.5B-Instruct, where
// `Revision=main` and `Revision=zzz-not-real` are indistinguishable from each
// other and differ from `Revision=master` only in that field. A caller that read
// that as "the repository is empty" would show a user an empty directory for
// what is actually a typo, and a downloader would fetch nothing and report
// success.
//
// So a null list is reported as ErrNotFound naming the revision. A genuinely
// empty repository is a real, separate answer and comes back as an empty slice
// with no error; only the hub's own "I have nothing to say about this revision"
// is an error. A repository that does not exist at all is a plain 404 and is
// mapped by responseError.
func (ms *modelScope) listRepoFiles(name, version string) ([]ModelScopeRepoFile, error) {
	params := url.Values{}
	// Root selects a subtree; empty means the repository root.
	params.Set("Root", "")

	if revision := ms.revision(version); revision != "" {
		params.Set("Revision", revision)
	}

	requestURL := fmt.Sprintf("%s%s/%s%s?%s",
		ms.url, modelScopeModelPath, name, modelScopeRepoFilesPath, params.Encode())

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	context := fmt.Sprintf("failed to list files of model %s", name)

	raw, err := ms.do(req, context)
	if err != nil {
		return nil, err
	}

	var tree modelScopeRepoTree
	if err := json.Unmarshal(raw, &tree); err != nil {
		return nil, errors.Wrap(err, context)
	}

	if tree.Files == nil {
		return nil, errors.Wrapf(ErrNotFound,
			"model %s has no revision %q", name, ms.describeRevision(version))
	}

	return *tree.Files, nil
}

// describeRevision names a revision in a message. The default is addressed by
// omitting it on the wire, which has no name to quote, so it is described as
// what the caller asked for.
func (ms *modelScope) describeRevision(version string) string {
	if revision := ms.revision(version); revision != "" {
		return revision
	}

	return v1.LatestVersion
}

// missingRevision reports the revision as absent, or nil if it is not provably
// absent.
//
// The file endpoint answers the same 404 for a file that is not in the
// repository and for a revision that does not exist, so "no config.json" and
// "you spelled the branch wrong" arrive identical. The tree endpoint can tell
// them apart, so it is asked — once, and only on a miss.
//
// Nil is returned when the tree lookup itself fails for any other reason: a
// secondary request must never replace a real answer with its own trouble.
func (ms *modelScope) missingRevision(name, version string) error {
	if _, err := ms.listRepoFiles(name, version); errors.Is(err, ErrNotFound) {
		return err
	}

	return nil
}

// GetModelDetail reads what a public checkpoint states about its own shape.
//
// It fetches config.json and nothing else — the weights are never downloaded —
// and parses it with modelmeta.ParseConfig, the same parser Hugging Face and the
// private path use, so a model reports the same shape whichever registry it came
// from.
//
// ModelScope publishes no equivalent of the Hub's safetensors tally, so the
// parameter count is reported as missing rather than derived from something that
// is not it: the detail endpoint's StorageSize is bytes on disk.
func (ms *modelScope) GetModelDetail(name, version string) (*v1.ModelVersion, error) {
	raw, err := ms.fetchFile(name, version, configFile)
	if err != nil {
		if !errors.Is(err, ErrNotFound) {
			return nil, err
		}

		// "No config.json" and "no such revision" reach here as the same 404, and
		// swallowing both would hand back a model whose every field is missing —
		// a mistyped revision rendered as a real checkpoint that happens to say
		// nothing about itself. Ask which one it was.
		if revErr := ms.missingRevision(name, version); revErr != nil {
			return nil, revErr
		}
	}

	// A repository with no config.json is a real answer, not a failure; the parser
	// treats an empty file as "every field missing", which is exactly right.
	info := modelmeta.ParseConfig(raw)
	info.MarkFieldMissing(v1.ModelInfoFieldParameterCount)

	return &v1.ModelVersion{
		Name: reportedVersion(version, modelScopeDefaultRevision),
		Info: info,
	}, nil
}

// GetReadme fetches the model card, returned exactly as stored, front matter
// and all.
//
// A repository with no README and a revision that does not exist answer the
// same 404 here, so the tree is consulted on a miss to say which it was — see
// missingRevision. Both are ErrNotFound; only the sentence differs, and it is
// the sentence the reader acts on.
func (ms *modelScope) GetReadme(name, version string) (*Readme, error) {
	raw, err := ms.fetchFile(name, version, readmeFileName)
	if err != nil {
		// "This model has no README" is the wrong thing to tell someone who
		// mistyped a revision, and the file endpoint reports both the same way.
		if errors.Is(err, ErrNotFound) {
			if revErr := ms.missingRevision(name, version); revErr != nil {
				return nil, revErr
			}
		}

		return nil, err
	}

	return readCappedReadme(bytes.NewReader(raw))
}

// fetchFile reads one file out of a repository, capped at MaxReadmeBytes. The
// cap applies to every file: these are small metadata files, and the read is from
// a server this deployment does not control.
//
// Revision is omitted for the default version rather than being set to a guessed
// branch name, because the hub resolves an absent Revision to the repository's
// own default, and a wrong guess is not a different name for the same thing —
// it is a 404 (see modelScopeDefaultRevision).
//
// The 404 this maps to ErrNotFound is ambiguous: it is the same answer for a
// file that is absent and for a revision that never existed. Callers that report
// the miss to a user resolve it with missingRevision.
func (ms *modelScope) fetchFile(name, version, file string) ([]byte, error) {
	params := url.Values{}
	params.Set("FilePath", file)

	if revision := ms.revision(version); revision != "" {
		params.Set("Revision", revision)
	}

	requestURL := fmt.Sprintf("%s%s/%s%s?%s",
		ms.url, modelScopeModelPath, name, modelScopeRepoFilePath, params.Encode())

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	resp, err := ms.request(req)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch %s of model %s", file, name)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.Wrapf(ErrNotFound, "model %s has no %s on ModelScope", name, file)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, ms.responseError(resp, fmt.Sprintf("failed to fetch %s of model %s", file, name))
	}

	// A file is served as its own bytes, not wrapped in the JSON envelope.
	return io.ReadAll(io.LimitReader(resp.Body, MaxReadmeBytes+1))
}

// revision maps a version onto the hub's wire name for it, and returns "" for
// "whatever the repository's default is" — see fetchFile. The name given back to
// a caller is reportedVersion's business, not this one's; the two are a pair, and
// the split is what keeps a listing and a detail from naming the same version
// differently.
func (ms *modelScope) revision(version string) string {
	if version == "" || version == v1.LatestVersion {
		return ""
	}

	return version
}

// do sends a request that answers in the standard envelope and returns its Data.
func (ms *modelScope) do(req *http.Request, context string) (json.RawMessage, error) {
	resp, err := ms.request(req)
	if err != nil {
		return nil, errors.Wrap(err, context)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, ms.responseError(resp, context)
	}

	var envelope modelScopeEnvelope
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxReadmeBytes)).Decode(&envelope); err != nil {
		return nil, errors.Wrap(err, context)
	}

	// A 200 whose envelope says otherwise is still a failure. The hub is
	// consistent about the status today, but the envelope is what it treats as
	// authoritative.
	if !envelope.Success && envelope.Code != http.StatusOK {
		return nil, errors.Errorf("%s: %s (code %d)", context, envelope.Message, envelope.Code)
	}

	return envelope.Data, nil
}

// request sends a request with the registry's credentials attached, retrying a
// 429 the same way the Hugging Face client does.
func (ms *modelScope) request(req *http.Request) (*http.Response, error) {
	if ms.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+ms.apiToken)
	}

	return ms.doWithRetry(req)
}

// doWithRetry sends a request, retrying a 429 and nothing else. Transport errors
// are left alone: the client already has a timeout, and an unreachable hub is
// what the reachability check reports.
//
// A retried request has to be replayable, so the body is rewound between
// attempts; every request this client sends carries a GetBody, because they are
// all built by http.NewRequest over a bytes.Reader or have no body at all.
func (ms *modelScope) doWithRetry(req *http.Request) (*http.Response, error) {
	var lastResp *http.Response

	for attempt := 0; attempt < hubRetryAttempts; attempt++ {
		if attempt > 0 && req.GetBody != nil {
			body, err := req.GetBody()
			if err != nil {
				return lastResp, nil
			}

			req.Body = body
		}

		resp, err := ms.client.Do(req)
		if err != nil {
			return nil, err
		}

		if resp.StatusCode != http.StatusTooManyRequests {
			return resp, nil
		}

		lastResp = resp

		if attempt == hubRetryAttempts-1 {
			break
		}

		// The body is not needed and the connection is worth reusing.
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, hubErrorBodyLimit)) //nolint:errcheck
		resp.Body.Close()

		time.Sleep(retryDelay(resp, attempt))
	}

	return lastResp, nil
}

// responseError turns a non-OK response into an error a caller can act on.
// Credentials and throttling get their own types because the remedies differ;
// anything else keeps the hub's own words.
func (ms *modelScope) responseError(resp *http.Response, context string) error {
	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint := "a token is required"
		if ms.apiToken != "" {
			hint = "the configured token was rejected or does not grant access"
		}

		return errors.Wrapf(ErrUnauthorized, "%s: %s (%s)", context, hint, resp.Status)
	case http.StatusTooManyRequests:
		return errors.Wrapf(ErrRateLimited, "%s: %s", context, resp.Status)
	default:
		return errors.Errorf("%s: %s%s", context, resp.Status, modelScopeErrorDetail(resp))
	}
}

// modelScopeErrorDetail quotes the hub's own explanation, when it gave one, in a
// form that can be appended to a message. The read is bounded because the
// endpoint may be a misconfigured proxy serving a whole HTML page.
func modelScopeErrorDetail(resp *http.Response) string {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, hubErrorBodyLimit)) //nolint:errcheck

	var envelope modelScopeEnvelope
	if err := json.Unmarshal(body, &envelope); err == nil && envelope.Message != "" {
		return fmt.Sprintf(": %s (code %d)", envelope.Message, envelope.Code)
	}

	detail := strings.TrimSpace(string(body))
	if detail == "" {
		return ""
	}

	return ": " + detail
}

// DeleteModel returns an error for ModelScope as it's read-only
func (ms *modelScope) DeleteModel(name, version string) error {
	return errModelScopeNotSupported
}

// ImportModel returns an error for ModelScope as it's read-only
func (ms *modelScope) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return errModelScopeNotSupported
}

// ExportModel returns an error for ModelScope as it's read-only
func (ms *modelScope) ExportModel(name, version, outputPath string) error {
	return errModelScopeNotSupported
}

// GetModelPath returns an error for ModelScope as it's read-only
func (ms *modelScope) GetModelPath(name, version string) (string, error) {
	return "", errModelScopeNotSupported
}

// SetManualModelInfo returns an error for ModelScope as it's read-only
func (ms *modelScope) SetManualModelInfo(name, version string, info *v1.ModelInfo) error {
	return errModelScopeNotSupported
}

// CollectUsage is not implemented for ModelScope: the hub's storage is not ours
// to measure, and the model count is unbounded.
func (ms *modelScope) CollectUsage() (*RegistryUsage, error) {
	return nil, errModelScopeNotSupported
}

func (ms *modelScope) GetNFSVersion() (string, error) {
	return "", nil
}
