package model_registry

import (
	"bytes"
	"crypto/tls"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/pkg/errors"

	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/model_registry/modelmeta"
)

var (
	sharedClient = func() *http.Client {
		transport := http.DefaultTransport.(*http.Transport).Clone() //nolint:errcheck
		transport.TLSClientConfig = &tls.Config{
			//nolint:gosec
			InsecureSkipVerify: true,
		}
		transport.IdleConnTimeout = 30 * time.Second

		return &http.Client{
			Timeout:   300 * time.Second,
			Transport: transport,
		}
	}()
)

const (
	listModelPath = "/api/models"
	whoamiPath    = "/api/whoami-v2"
	configFile    = "config.json"
	// defaultRevision is the branch a Hub repository serves when no revision is
	// named. Our listings report every Hub model as v1.LatestVersion, which is the
	// same idea under a different name, so that is what it maps to.
	defaultRevision = "main"
)

// errHuggingFaceNotSupported wraps ErrNotSupported so a caller can tell "a
// public registry cannot do this" from "it tried and failed", and answer with a
// clear refusal instead of a server error. Hugging Face is read-only here; the
// write and detail operations are not implemented against it.
var errHuggingFaceNotSupported = errors.Wrap(ErrNotSupported, "operation not supported for Hugging Face registry")

var (
	// ErrUnauthorized is returned when the hub refused the request because of who
	// is asking: no token where one is needed, or a token that is expired or does
	// not carry access to a gated repository. It is kept apart from a generic
	// failure because the remedy is specific and it is the user's to apply — no
	// amount of retrying will help.
	ErrUnauthorized = errors.New("model registry rejected the credentials")
	// ErrRateLimited is returned when the hub asked us to slow down and was still
	// asking after the retries below. Also distinct on purpose: it says the
	// registry is working and the request is fine, which is neither a credential
	// problem nor an outage, and the answer is to wait.
	ErrRateLimited = errors.New("model registry is rate limiting requests")
)

const (
	// hubRetryAttempts is how many times a rate-limited request is retried before
	// giving up. Deliberately small: this runs inside an HTTP handler somebody is
	// waiting on, so the budget is a couple of seconds, not a couple of minutes.
	// Past that the honest answer is "the hub is rate limiting", which the caller
	// can act on.
	hubRetryAttempts = 3
	// hubRetryBaseDelay is the first backoff step; it doubles per attempt. The
	// hub's own Retry-After takes precedence whenever it sends one.
	hubRetryBaseDelay = 300 * time.Millisecond
	// hubRetryMaxDelay caps a Retry-After we are willing to sit out inline. A hub
	// asking for a minute is telling us to come back later, not to hold a request
	// open for a minute.
	hubRetryMaxDelay = 2 * time.Second
	// hubErrorBodyLimit is how much of an error body is read to quote in the
	// message. Enough for the hub's own explanation, bounded because the endpoint
	// might be a misconfigured proxy serving a whole HTML page.
	hubErrorBodyLimit = 1024
)

type huggingFace struct {
	apiToken string
	client   *http.Client
	url      string
}

func newHuggingFace(registry *v1.ModelRegistry) (*huggingFace, error) {
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

	return &huggingFace{
		url:      strings.TrimSuffix(parsedUrl.String(), "/"),
		apiToken: registry.Spec.Credentials,
		client:   sharedClient,
	}, nil
}

func (hf *huggingFace) Connect() error {
	// Perform a health check to validate the connection
	return hf.healthyCheck()
}

func (hf *huggingFace) Disconnect() error {
	return nil
}

type HuggingFaceModel struct {
	ID            string    `json:"_id"`
	ID0           string    `json:"id"`
	Likes         int       `json:"likes,omitempty"`
	TrendingScore float64   `json:"trendingScore,omitempty"`
	Private       bool      `json:"private,omitempty"`
	Downloads     int       `json:"downloads,omitempty"`
	Tags          []string  `json:"tags,omitempty"`
	PipelineTag   string    `json:"pipeline_tag,omitempty"`
	LibraryName   string    `json:"library_name,omitempty"`
	CreatedAt     time.Time `json:"createdAt,omitempty"`
	ModelID       string    `json:"modelId,omitempty"`
}

// ListModels retrieves the first page of models from the Hugging Face Hub API.
//
// Offset is refused rather than ignored. The Hub's supported pagination is an
// opaque, server-generated cursor carried in a Link header, which cannot express
// "start at row N"; its `skip` parameter, which could, is documented by the Hub
// itself as deprecated and rejects values past a few thousand. Silently serving
// the first page for every offset would make a paging client believe it was
// walking the catalogue while it re-read the same rows.
//
// The Hub also does not report how many models matched, so Total is unknown.
func (hf *huggingFace) ListModels(option ListOption) (*ModelPage, error) {
	var (
		allHFModels []HuggingFaceModel
		result      = []v1.GeneralModel{}
	)

	if option.Offset > 0 {
		return nil, errors.Wrap(ErrNotSupported, "the Hugging Face Hub API cannot list models from an offset")
	}

	allHFModels, err := hf.getModelsList(option)
	if err != nil {
		return nil, err
	}

	for i := range allHFModels {
		result = append(result, v1.GeneralModel{
			Name: allHFModels[i].ModelID,
			Versions: []v1.ModelVersion{
				{
					Name:         v1.LatestVersion,
					CreationTime: allHFModels[i].CreatedAt.Format(time.RFC3339Nano),
				},
			},
		})
	}

	return &ModelPage{Models: result}, nil
}

// HealthyCheck checks the health of the Hugging Face Hub API.
func (hf *huggingFace) HealthyCheck() error {
	return hf.healthyCheck()
}

func (hf *huggingFace) healthyCheck() error {
	if hf.apiToken != "" {
		// Validate the token by making a simple request
		_, err := hf.whoami()
		if err != nil {
			return errors.Wrap(err, "invalid Hugging Face API token")
		}
	}

	_, err := hf.getModelsList(ListOption{Search: "", Limit: 1})
	if err != nil {
		return errors.Wrap(err, "failed to list models from Hugging Face API")
	}

	return nil
}

// GetModelsList calls the Hugging Face Hub API to get a list of models with pagination.
func (hf *huggingFace) getModelsList(options ListOption) ([]HuggingFaceModel, error) {
	params := url.Values{}
	if options.Limit != 0 {
		params.Add("limit", strconv.Itoa(options.Limit))
	}

	if options.Search != "" {
		params.Add("search", options.Search)
	}

	requestURL := hf.url + listModelPath + "?" + params.Encode()

	req, err := http.NewRequest("GET", requestURL, nil)
	if err != nil {
		return nil, err
	}

	if hf.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+hf.apiToken)
	}

	resp, err := hf.doWithRetry(req)
	if err != nil {
		return nil, errors.Wrap(err, "failed to list models")
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return nil, hf.responseError(resp, "failed to list models")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}

	var models []HuggingFaceModel

	if err = json.Unmarshal(body, &models); err != nil {
		return nil, err
	}

	return models, nil
}

func (hf *huggingFace) whoami() (string, error) {
	req, err := http.NewRequest("GET", hf.url+whoamiPath, nil)
	if err != nil {
		return "", err
	}

	req.Header.Set("Authorization", "Bearer "+hf.apiToken)

	resp, err := hf.doWithRetry(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", hf.responseError(resp, "failed to validate Hugging Face API token")
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}

	var result struct {
		Name string `json:"name"`
	}

	if err = json.Unmarshal(body, &result); err != nil {
		return "", err
	}

	return result.Name, nil
}

// Implement the remaining ModelRegistry interface methods with "not supported" errors

func (hf *huggingFace) GetModelVersion(name, version string) (*v1.ModelVersion, error) {
	return nil, errHuggingFaceNotSupported
}

// GetModelDetail reads what a public checkpoint states about its own shape.
//
// It fetches one file — config.json — and nothing else. That file carries the
// architecture, the layer and head counts, head_dim, the expert counts and the
// quantization width, which is everything the resource estimate needs, and it is
// a few kilobytes next to a checkpoint measured in gigabytes. The weights are
// never downloaded, and the same parser reads it as reads a private
// checkpoint's, so a model has the same shape whichever registry it came from.
//
// The parameter count comes from a second call, to the Hub's own tally of the
// safetensors headers. That number is the same sum the private path computes by
// reading those headers itself, so it is recorded as auto rather than as an
// estimate — but it is fetched separately and treated as optional: a hub that is
// slow, throttling or simply does not report it leaves the field in
// missing_fields. A detail view is not worth failing over a field that is
// allowed to be unknown.
func (hf *huggingFace) GetModelDetail(name, version string) (*v1.ModelVersion, error) {
	raw, err := hf.fetchFile(name, version, configFile)
	if err != nil && !errors.Is(err, ErrNotFound) {
		return nil, err
	}

	// A repository with no config.json is a real answer, not a failure: plenty of
	// things on the Hub are not transformers checkpoints. It yields a model whose
	// every field is missing, which is what the parser does with an empty file
	// anyway.
	info := modelmeta.ParseConfig(raw)

	if total, ok := hf.parameterCount(name, version); ok {
		info.ParameterCount = strconv.FormatInt(total, 10)
		info.SetFieldSource(v1.ModelInfoFieldParameterCount, v1.ModelInfoSourceAuto)
	} else {
		info.MarkFieldMissing(v1.ModelInfoFieldParameterCount)
	}

	return &v1.ModelVersion{
		Name: hf.revision(version),
		Info: info,
	}, nil
}

// safetensorsInfo is the Hub's tally of a repository's safetensors headers:
// element counts per dtype and their total. It is the same quantity the private
// path sums out of the headers on disk, which is why the field can be reported
// as read rather than estimated.
type safetensorsInfo struct {
	Total *int64 `json:"total"`
}

// parameterCount asks the Hub for the model's stored tensor total.
//
// Every failure here is the same answer — "not known" — and none of them is
// worth an error. The count is one field on a detail view, the view is useful
// without it, and the alternative is a page that fails outright because a
// secondary request timed out. A hub that does not report the field at all
// (anything not stored as safetensors) lands in the same place.
func (hf *huggingFace) parameterCount(name, version string) (int64, bool) {
	requestURL := hf.url + listModelPath + "/" + name
	if revision := hf.revision(version); revision != defaultRevision {
		requestURL += "/revision/" + url.PathEscape(revision)
	}

	requestURL += "?expand%5B%5D=safetensors"

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return 0, false
	}

	if hf.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+hf.apiToken)
	}

	resp, err := hf.doWithRetry(req)
	if err != nil {
		klog.V(4).Infof("no parameter count for model %s: %v", name, err)

		return 0, false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		klog.V(4).Infof("no parameter count for model %s: %s", name, resp.Status)

		return 0, false
	}

	var payload struct {
		Safetensors *safetensorsInfo `json:"safetensors"`
	}

	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxReadmeBytes)).Decode(&payload); err != nil {
		klog.V(4).Infof("no parameter count for model %s: %v", name, err)

		return 0, false
	}

	if payload.Safetensors == nil || payload.Safetensors.Total == nil || *payload.Safetensors.Total <= 0 {
		return 0, false
	}

	return *payload.Safetensors.Total, true
}

// GetReadme fetches the model card from the Hub.
//
// Unlike the rest of a public model's metadata, the README is a single small
// file the Hub serves directly, so reading it costs one request and downloads
// nothing else. It is returned exactly as stored, front matter and all: the Hub
// stores markdown, this returns markdown.
//
// A repository that exists but has no README, and a repository that does not
// exist, both answer 404 here — the Hub does not distinguish them on a file
// path, and treating "cannot see it" as "has none" is the honest reading for a
// caller who is being told the model card is missing either way.
func (hf *huggingFace) GetReadme(name, version string) (*Readme, error) {
	raw, err := hf.fetchFile(name, version, readmeFileName)
	if err != nil {
		return nil, err
	}

	return readCappedReadme(bytes.NewReader(raw))
}

// revision maps our version onto the Hub's. Our listings report every Hub model
// as v1.LatestVersion, which is the same idea as the repository default branch
// under a different name; anything else is passed through as a branch, tag or
// commit.
func (hf *huggingFace) revision(version string) string {
	if version == "" || version == v1.LatestVersion {
		return defaultRevision
	}

	return version
}

// doWithRetry sends a request, retrying only what is worth retrying: a 429, and
// only for as long as somebody waiting on an HTTP handler will tolerate. A
// transport error is not retried here — the client already has a timeout, and a
// hub that is unreachable is what the reachability check is for.
func (hf *huggingFace) doWithRetry(req *http.Request) (*http.Response, error) {
	var lastResp *http.Response

	for attempt := 0; attempt < hubRetryAttempts; attempt++ {
		resp, err := hf.client.Do(req)
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

// retryDelay honours the hub's Retry-After when it sends one, capped, and
// otherwise backs off exponentially. Doing as we are told is the point of the
// header; the cap is because this is happening inline.
func retryDelay(resp *http.Response, attempt int) time.Duration {
	if after := resp.Header.Get("Retry-After"); after != "" {
		if seconds, err := strconv.Atoi(after); err == nil && seconds >= 0 {
			delay := time.Duration(seconds) * time.Second
			if delay > hubRetryMaxDelay {
				return hubRetryMaxDelay
			}

			return delay
		}
	}

	delay := hubRetryBaseDelay << attempt
	if delay > hubRetryMaxDelay {
		return hubRetryMaxDelay
	}

	return delay
}

// responseError turns a non-OK hub response into an error a caller can act on.
//
// The three cases that get their own type are the three a user can do something
// about and that the generic "it failed" hides: the request needs a token, the
// hub is throttling, or the hub is not answering. Everything else keeps the
// hub's own words, which are usually more specific than anything invented here.
func (hf *huggingFace) responseError(resp *http.Response, context string) error {
	body, _ := io.ReadAll(io.LimitReader(resp.Body, hubErrorBodyLimit)) //nolint:errcheck
	detail := strings.TrimSpace(string(body))

	switch resp.StatusCode {
	case http.StatusUnauthorized, http.StatusForbidden:
		hint := "a token is required"
		if hf.apiToken != "" {
			hint = "the configured token was rejected or does not grant access"
		}

		return errors.Wrapf(ErrUnauthorized, "%s: %s (%s)", context, hint, resp.Status)
	case http.StatusTooManyRequests:
		return errors.Wrapf(ErrRateLimited, "%s: %s", context, resp.Status)
	default:
		return errors.Errorf("%s: %s: %s", context, resp.Status, detail)
	}
}

// fetchFile reads one file out of a Hub repository, capped at MaxReadmeBytes.
//
// The cap applies to every file this fetches on purpose: these are small
// metadata files, and the alternative is an unbounded read from a server we do
// not control — a misconfigured HF_ENDPOINT pointing at something that streams
// forever is a configuration mistake, not a reason to exhaust memory.
func (hf *huggingFace) fetchFile(name, version, file string) ([]byte, error) {
	requestURL := fmt.Sprintf("%s/%s/resolve/%s/%s", hf.url, name, url.PathEscape(hf.revision(version)), file)

	req, err := http.NewRequest(http.MethodGet, requestURL, nil)
	if err != nil {
		return nil, err
	}

	if hf.apiToken != "" {
		req.Header.Set("Authorization", "Bearer "+hf.apiToken)
	}

	resp, err := hf.doWithRetry(req)
	if err != nil {
		return nil, errors.Wrapf(err, "failed to fetch %s of model %s", file, name)
	}
	defer resp.Body.Close()

	if resp.StatusCode == http.StatusNotFound {
		return nil, errors.Wrapf(ErrNotFound, "model %s has no %s on the Hugging Face Hub", name, file)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, hf.responseError(resp, fmt.Sprintf("failed to fetch %s of model %s", file, name))
	}

	return io.ReadAll(io.LimitReader(resp.Body, MaxReadmeBytes+1))
}

// DeleteModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) DeleteModel(name, version string) error {
	return errHuggingFaceNotSupported
}

// ImportModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) ImportModel(reader io.Reader, name, version string, progress io.Writer) error {
	return errHuggingFaceNotSupported
}

// ExportModel returns an error for HuggingFace as it's read-only
func (hf *huggingFace) ExportModel(name, version, outputPath string) error {
	return errHuggingFaceNotSupported
}

// GetModelPath returns an error for HuggingFace as it's read-only
func (hf *huggingFace) GetModelPath(name, version string) (string, error) {
	return "", errHuggingFaceNotSupported
}

// SetManualModelInfo returns an error for HuggingFace as it's read-only
func (hf *huggingFace) SetManualModelInfo(name, version string, info *v1.ModelInfo) error {
	return errHuggingFaceNotSupported
}

// CollectUsage is not implemented for HuggingFace: the Hub's storage is not ours
// to measure, and the model count is unbounded.
func (hf *huggingFace) CollectUsage() (*RegistryUsage, error) {
	return nil, errHuggingFaceNotSupported
}

func (hf *huggingFace) GetNFSVersion() (string, error) {
	return "", nil
}
