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
	// ErrUnauthorized is returned when the hub refused the request over
	// credentials: no token where one is needed, or one that is expired or lacks
	// access. Retrying never helps, so callers must not treat it as an outage.
	ErrUnauthorized = errors.New("model registry rejected the credentials")
	// ErrRateLimited is returned when the hub was still throttling after the
	// retries below. The registry is working and the request is valid; the answer
	// is to come back later.
	ErrRateLimited = errors.New("model registry is rate limiting requests")
)

const (
	// These bound the retry budget. It is small because the retries happen inside
	// an HTTP handler somebody is waiting on: past a couple of seconds the useful
	// answer is ErrRateLimited, which the caller can act on.
	hubRetryAttempts  = 3
	hubRetryBaseDelay = 300 * time.Millisecond
	hubRetryMaxDelay  = 2 * time.Second
	// hubErrorBodyLimit bounds how much of an error body is quoted, since the
	// endpoint may be a misconfigured proxy serving a whole HTML page.
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
// It fetches config.json and nothing else — the weights are never downloaded —
// and parses it with modelmeta.ParseConfig, the same parser the private path
// uses, so a model reports the same shape whichever registry it came from.
//
// The parameter count comes from a second, optional call to the hub's tally of
// the safetensors headers. Any failure there leaves the field in missing_fields
// rather than failing the request: a detail view is useful without it.
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
		Name: reportedVersion(version, defaultRevision),
		Info: info,
	}, nil
}

// safetensorsInfo is the hub's tally of a repository's safetensors headers. It
// is the same quantity the private path sums from the headers on disk, which is
// why the value is recorded as read rather than estimated.
type safetensorsInfo struct {
	Total *int64 `json:"total"`
}

// parameterCount asks the hub for the model's stored tensor total. Every failure
// — including a repository not stored as safetensors — is reported as "not
// known" rather than as an error, so a secondary request can never fail the
// detail view.
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

// GetReadme fetches the model card, returned exactly as stored, front matter
// and all.
//
// A repository with no README and a repository that does not exist both answer
// 404 on a file path, so both surface as ErrNotFound; the hub gives no way to
// tell them apart here.
func (hf *huggingFace) GetReadme(name, version string) (*Readme, error) {
	raw, err := hf.fetchFile(name, version, readmeFileName)
	if err != nil {
		return nil, err
	}

	return readCappedReadme(bytes.NewReader(raw))
}

// revision maps a version onto the hub's wire name for it: v1.LatestVersion
// stands for the repository's default branch, anything else is passed through as
// a branch, tag or commit.
func (hf *huggingFace) revision(version string) string {
	if version == "" || version == v1.LatestVersion {
		return defaultRevision
	}

	return version
}

// reportedVersion is the version name given back to callers, and it must be the
// one ListModels emits: a version read from a listing is what a client puts into
// "model get <name>:<version>" and into a detail URL, so a detail that answered
// with the registry's own branch name would name a version the listing never
// shows and that a subsequent lookup would not resolve. The branch name stays on
// the wire only — see each provider's revision.
//
// The rule is one rule and lives here. What a registry calls its default branch
// is not part of it — it is provider knowledge, and it differs: the Hub's is
// "main", ModelScope's is "master". Hard-coding either would report a real
// branch of the other registry as "latest", which is both wrong and a version
// name that registry would not resolve. Pass "" for a registry with no single
// name for its default.
func reportedVersion(version, defaultBranch string) string {
	if version == "" {
		return v1.LatestVersion
	}

	if defaultBranch != "" && version == defaultBranch {
		return v1.LatestVersion
	}

	return version
}

// doWithRetry sends a request, retrying a 429 and nothing else. Transport errors
// are left alone: the client already has a timeout, and an unreachable hub is
// what the reachability check reports.
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

// retryDelay honours the hub's Retry-After when present, capped at
// hubRetryMaxDelay, and otherwise backs off exponentially.
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
// Credentials and throttling get their own types because the remedies differ;
// anything else keeps the hub's own words.
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

// fetchFile reads one file out of a hub repository, capped at MaxReadmeBytes.
// The cap applies to every file: these are small metadata files, and the read is
// from a server this deployment does not control.
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
