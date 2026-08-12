package model_registry

import (
	"bytes"
	"errors"
	"io"
	"net/http"
	"testing"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/stretchr/testify/assert"
)

type MockRoundTripper struct {
	RoundTripFunc func(req *http.Request) (*http.Response, error)
}

func (m *MockRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	return m.RoundTripFunc(req)
}

func TestNewHuggingFace(t *testing.T) {
	tests := []struct {
		name          string
		registry      *v1.ModelRegistry
		wantErr       bool
		wantErrString string
	}{
		{
			name: "registry with empty url",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "",
				},
			},
			wantErr:       true,
			wantErrString: "cannot be empty",
		},
		{
			name: "registry with invalid url, no scheme",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "invalid-url",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, no host",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "http://",
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "registry with valid url, unsupport character",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url: `
					`,
				},
			},
			wantErr:       true,
			wantErrString: "invalid registry.Spec.Url",
		},
		{
			name: "normal registry",
			registry: &v1.ModelRegistry{
				Spec: &v1.ModelRegistrySpec{
					Type: v1.HuggingFaceModelRegistryType,
					Url:  "https://huggingface.co",
				},
			},
			wantErr:       false,
			wantErrString: "",
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := newHuggingFace(tt.registry)
			if tt.wantErr {
				assert.ErrorContains(t, err, tt.wantErrString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

func TestHuggingFace_HealthyCheck(t *testing.T) {
	tests := []struct {
		name          string
		apiToken      string
		mockResponse  func(req *http.Request) (*http.Response, error)
		wantErr       bool
		wantErrString string
	}{
		{
			name:     "success without token",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`[{"modelId": "test-model"}]`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr: false,
		},
		{
			name:     "success with valid token",
			apiToken: "valid-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					assert.Equal(t, "Bearer valid-token", req.Header.Get("Authorization"))
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`{"name": "test-user"}`)),
						Header:     make(http.Header),
					}, nil
				}
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`[{"modelId": "test-model"}]`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr: false,
		},
		{
			name:     "invalid token - whoami fails",
			apiToken: "invalid-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return &http.Response{
						StatusCode: http.StatusUnauthorized,
						Body:       io.NopCloser(bytes.NewBufferString(`{"error": "Invalid token"}`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "list models fails",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusInternalServerError,
						Body:       io.NopCloser(bytes.NewBufferString(`{"error": "Internal server error"}`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
		{
			name:     "network error on whoami",
			apiToken: "test-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return nil, errors.New("network timeout")
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "network error on list models",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return nil, errors.New("connection refused")
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
		{
			name:     "invalid json response from whoami",
			apiToken: "test-token",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/whoami-v2" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`invalid json`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "invalid Hugging Face API token",
		},
		{
			name:     "invalid json response from list models",
			apiToken: "",
			mockResponse: func(req *http.Request) (*http.Response, error) {
				if req.URL.Path == "/api/models" {
					return &http.Response{
						StatusCode: http.StatusOK,
						Body:       io.NopCloser(bytes.NewBufferString(`not a valid json`)),
						Header:     make(http.Header),
					}, nil
				}
				return nil, errors.New("unexpected request")
			},
			wantErr:       true,
			wantErrString: "failed to list models from Hugging Face API",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			// Create mock HTTP client
			mockClient := &http.Client{
				Transport: &MockRoundTripper{
					RoundTripFunc: tt.mockResponse,
				},
			}

			hf := &huggingFace{
				url:      "https://huggingface.co",
				apiToken: tt.apiToken,
				client:   mockClient,
			}

			err := hf.healthyCheck()

			if tt.wantErr {
				assert.Error(t, err)
				assert.Contains(t, err.Error(), tt.wantErrString)
			} else {
				assert.NoError(t, err)
			}
		})
	}
}

// A public registry refuses the operations it does not implement with a typed
// error, so a caller can answer "this registry kind cannot do that" instead of
// reporting a server failure.
func TestHuggingFace_UnsupportedOperationsAreTyped(t *testing.T) {
	hf := &huggingFace{url: "https://huggingface.co"}

	t.Run("SetManualModelInfo", func(t *testing.T) {
		assert.ErrorIs(t, hf.SetManualModelInfo("qwen3", "latest", &v1.ModelInfo{}), ErrNotSupported)
	})

	t.Run("CollectUsage", func(t *testing.T) {
		_, err := hf.CollectUsage()
		assert.ErrorIs(t, err, ErrNotSupported)
	})

	t.Run("GetModelVersion", func(t *testing.T) {
		_, err := hf.GetModelVersion("qwen3", "latest")
		assert.ErrorIs(t, err, ErrNotSupported)
	})

	t.Run("DeleteModel", func(t *testing.T) {
		assert.ErrorIs(t, hf.DeleteModel("qwen3", "latest"), ErrNotSupported)
	})

	t.Run("GetModelPath", func(t *testing.T) {
		_, err := hf.GetModelPath("qwen3", "latest")
		assert.ErrorIs(t, err, ErrNotSupported)
	})

	// The wording predates the typed error and is kept so existing messages do
	// not change. Asserted against an operation that is still refused: model
	// detail used to be one and no longer is.
	err := hf.SetManualModelInfo("qwen3", "latest", &v1.ModelInfo{})
	assert.Contains(t, err.Error(), "operation not supported for Hugging Face registry")
}

// Paging a public registry from an offset is refused rather than silently
// answered with the first page. The Hub's own pagination is an opaque cursor
// that cannot express "start at row N", and its deprecated skip parameter stops
// working past a few thousand rows — so a client that believed an offset had
// been honoured would re-read the same models while thinking it was advancing.
func TestHuggingFace_ListModelsRefusesAnOffset(t *testing.T) {
	hf := &huggingFace{url: "https://huggingface.co"}

	page, err := hf.ListModels(ListOption{Offset: 10, Limit: 5})

	assert.Nil(t, page)
	assert.ErrorIs(t, err, ErrNotSupported)
}

// The Hub does not report how many models matched, so the total is unknown
// rather than the size of the page that came back.
func TestHuggingFace_ListModelsReportsAnUnknownTotal(t *testing.T) {
	hf := &huggingFace{
		url: "https://huggingface.co",
		client: &http.Client{
			Transport: &MockRoundTripper{
				RoundTripFunc: func(req *http.Request) (*http.Response, error) {
					assert.NotContains(t, req.URL.RawQuery, "offset")
					assert.NotContains(t, req.URL.RawQuery, "skip")

					return &http.Response{
						StatusCode: http.StatusOK,
						Body: io.NopCloser(bytes.NewBufferString(
							`[{"modelId":"a"},{"modelId":"b"}]`)),
					}, nil
				},
			},
		},
	}

	page, err := hf.ListModels(ListOption{Limit: 2})
	assert.NoError(t, err)
	assert.Len(t, page.Models, 2)
	assert.Nil(t, page.Total, "the Hub states no match count, so none may be invented")
}

// readmeResponder answers the model-card path and records what was asked for.
func readmeResponder(t *testing.T, status int, body string) (*http.Client, *string) {
	t.Helper()

	var requested string

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			requested = req.URL.String()

			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Header:     make(http.Header),
			}, nil
		},
	}}

	return client, &requested
}

// The model card is the one part of a public model that can be read without
// downloading the checkpoint, so it is served rather than refused.
func TestHuggingFace_GetReadme(t *testing.T) {
	client, requested := readmeResponder(t, http.StatusOK, "---\nlicense: apache-2.0\n---\n# Qwen3\n")
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	readme, err := hf.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	assert.NoError(t, err)
	// Verbatim markdown, front matter included: the server renders nothing.
	assert.Equal(t, "---\nlicense: apache-2.0\n---\n# Qwen3\n", readme.Content)
	assert.False(t, readme.Truncated)
	// "latest" is our name for what the Hub calls the default branch.
	assert.Equal(t, "https://huggingface.co/Qwen/Qwen3-8B/resolve/main/README.md", *requested)
}

func TestHuggingFace_GetReadmeUsesTheRequestedRevision(t *testing.T) {
	client, requested := readmeResponder(t, http.StatusOK, "# Qwen3\n")
	hf := &huggingFace{url: "https://hf-mirror.example", client: client}

	_, err := hf.GetReadme("Qwen/Qwen3-8B", "refs/pr/1")
	assert.NoError(t, err)
	assert.Equal(t, "https://hf-mirror.example/Qwen/Qwen3-8B/resolve/refs%2Fpr%2F1/README.md", *requested)
}

// "there is no model card" has to be distinguishable from "the hub could not be
// asked", or a caller cannot tell the user which one happened.
func TestHuggingFace_GetReadmeMissingIsNotFound(t *testing.T) {
	client, _ := readmeResponder(t, http.StatusNotFound, "Entry not found")
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	_, err := hf.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	assert.ErrorIs(t, err, ErrNotFound)
}

// A refusal is not a missing card. TestHuggingFace_UnauthorizedIsTyped covers
// what kind of refusal it is; this pins the part a caller branches on first.
func TestHuggingFace_GetReadmeRefusalIsNotNotFound(t *testing.T) {
	client, _ := readmeResponder(t, http.StatusForbidden, "gated repository")
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	_, err := hf.GetReadme("meta-llama/Llama-3-8B", v1.LatestVersion)
	assert.Error(t, err)
	assert.NotErrorIs(t, err, ErrNotFound)
}

func TestHuggingFace_GetReadmeIsCapped(t *testing.T) {
	client, _ := readmeResponder(t, http.StatusOK, string(bytes.Repeat([]byte("a"), MaxReadmeBytes+4096)))
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	readme, err := hf.GetReadme("Qwen/Qwen3-8B", v1.LatestVersion)
	assert.NoError(t, err)
	assert.True(t, readme.Truncated)
	assert.Len(t, readme.Content, MaxReadmeBytes)
}

// responderFor answers a fixed status/body and records every URL requested, so
// a test can assert both the outcome and how many times the hub was asked.
func responderFor(status int, body string, header http.Header) (*http.Client, *[]string) {
	requested := []string{}

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			requested = append(requested, req.URL.String())

			h := header
			if h == nil {
				h = make(http.Header)
			}

			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Header:     h,
			}, nil
		},
	}}

	return client, &requested
}

const qwen3Config = `{
  "architectures": ["Qwen3ForCausalLM"],
  "num_hidden_layers": 36,
  "num_attention_heads": 32,
  "num_key_value_heads": 8,
  "hidden_size": 4096,
  "max_position_embeddings": 40960,
  "torch_dtype": "bfloat16"
}`

// routeResponder answers each URL from a table, so a test can drive the two
// requests a detail view makes independently. An unlisted URL is a 404, which is
// how "the hub does not have this" is expressed.
func routeResponder(routes map[string]string) (*http.Client, *[]string) {
	requested := []string{}

	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			url := req.URL.String()
			requested = append(requested, url)

			body, ok := routes[url]
			status := http.StatusOK

			if !ok {
				body, status = "Entry not found", http.StatusNotFound
			}

			return &http.Response{
				StatusCode: status,
				Body:       io.NopCloser(bytes.NewBufferString(body)),
				Header:     make(http.Header),
			}, nil
		},
	}}

	return client, &requested
}

const (
	qwen3ConfigURL     = "https://huggingface.co/Qwen/Qwen3-8B/resolve/main/config.json"
	qwen3SafetensorURL = "https://huggingface.co/api/models/Qwen/Qwen3-8B?expand%5B%5D=safetensors"
)

// The shape of a public model is readable from one small file, and its exact
// parameter count from the hub's own tally of the weight headers. No weights are
// downloaded either way.
func TestHuggingFace_GetModelDetail(t *testing.T) {
	client, requested := routeResponder(map[string]string{
		qwen3ConfigURL:     qwen3Config,
		qwen3SafetensorURL: `{"id":"Qwen/Qwen3-8B","safetensors":{"parameters":{"BF16":8190735360},"total":8190735360}}`,
	})
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	detail, err := hf.GetModelDetail("Qwen/Qwen3-8B", v1.LatestVersion)
	assert.NoError(t, err)

	assert.Equal(t, []string{qwen3ConfigURL, qwen3SafetensorURL}, *requested,
		"one config file and one metadata call, nothing else")

	assert.Equal(t, "Qwen3ForCausalLM", detail.Info.Architecture)
	assert.Equal(t, 36, *detail.Info.NumHiddenLayers)
	assert.Equal(t, 8, *detail.Info.NumKeyValueHeads)
	// hidden_size / num_attention_heads, the one derivation the parser allows.
	assert.Equal(t, 128, *detail.Info.HeadDim)
	assert.Equal(t, v1.ModelInfoSourceDerived, detail.Info.FieldSources[v1.ModelInfoFieldHeadDim])
	assert.Equal(t, v1.ModelInfoSourceAuto, detail.Info.FieldSources[v1.ModelInfoFieldNumHiddenLayers])

	// The exact count, reported as read rather than estimated: it is the same sum
	// over the same headers that the private path computes for itself.
	assert.Equal(t, "8190735360", detail.Info.ParameterCount)
	assert.Equal(t, v1.ModelInfoSourceAuto, detail.Info.FieldSources[v1.ModelInfoFieldParameterCount])
	assert.NotContains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)
}

// The count is one field on a page that is useful without it. Every way of not
// getting it is the same answer — unknown — and none of them fails the request.
func TestHuggingFace_GetModelDetailWithoutAParameterCount(t *testing.T) {
	tests := []struct {
		name      string
		responses map[string]string
	}{
		{
			// Not stored as safetensors: GGUF, pickle, anything else.
			name: "the hub does not report one",
			responses: map[string]string{
				qwen3ConfigURL:     qwen3Config,
				qwen3SafetensorURL: `{"id":"Qwen/Qwen3-8B"}`,
			},
		},
		{
			name: "the metadata call fails",
			responses: map[string]string{
				qwen3ConfigURL: qwen3Config,
			},
		},
		{
			name: "the metadata call answers something unparseable",
			responses: map[string]string{
				qwen3ConfigURL:     qwen3Config,
				qwen3SafetensorURL: `<!doctype html><title>nope</title>`,
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := routeResponder(tt.responses)
			hf := &huggingFace{url: "https://huggingface.co", client: client}

			detail, err := hf.GetModelDetail("Qwen/Qwen3-8B", v1.LatestVersion)
			assert.NoError(t, err, "a missing parameter count must not fail the detail view")

			// The rest of the shape is still there.
			assert.Equal(t, "Qwen3ForCausalLM", detail.Info.Architecture)
			assert.Equal(t, 36, *detail.Info.NumHiddenLayers)

			assert.Empty(t, detail.Info.ParameterCount)
			assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)
			assert.NotContains(t, detail.Info.FieldSources, v1.ModelInfoFieldParameterCount)
		})
	}
}

// A named revision is asked about as that revision, not as the default branch.
func TestHuggingFace_ParameterCountFollowsTheRevision(t *testing.T) {
	client, requested := routeResponder(map[string]string{})
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	_, err := hf.GetModelDetail("Qwen/Qwen3-8B", "refs/pr/1")
	assert.NoError(t, err)

	assert.Contains(t, *requested,
		"https://huggingface.co/api/models/Qwen/Qwen3-8B/revision/refs%2Fpr%2F1?expand%5B%5D=safetensors")
}

// Plenty of repositories on the Hub are not transformers checkpoints. That is an
// answer, not a failure — and nothing is filled in from the repository name.
func TestHuggingFace_GetModelDetailWithoutConfig(t *testing.T) {
	client, _ := responderFor(http.StatusNotFound, "Entry not found", nil)
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	detail, err := hf.GetModelDetail("someone/qwen3-8b-gguf", v1.LatestVersion)
	assert.NoError(t, err)

	assert.Empty(t, detail.Info.Architecture)
	// Nothing is filled in from "qwen3-8b" in the repository name.
	assert.Empty(t, detail.Info.ParameterCount)
	assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldArchitecture)
	assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldParameterCount)
	assert.Contains(t, detail.Info.MissingFields, v1.ModelInfoFieldNumHiddenLayers)
}

// "It needs a token" is a different thing from "it broke", and only the first
// has an action attached to it.
func TestHuggingFace_UnauthorizedIsTyped(t *testing.T) {
	tests := []struct {
		name   string
		status int
		token  string
		hint   string
	}{
		{name: "no token configured", status: http.StatusUnauthorized, hint: "a token is required"},
		{name: "token rejected", status: http.StatusForbidden, token: "hf_bad", hint: "was rejected"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client, _ := responderFor(tt.status, "gated repository", nil)
			hf := &huggingFace{url: "https://huggingface.co", client: client, apiToken: tt.token}

			_, err := hf.GetReadme("meta-llama/Llama-3-8B", v1.LatestVersion)
			assert.ErrorIs(t, err, ErrUnauthorized)
			assert.Contains(t, err.Error(), tt.hint)

			_, listErr := hf.ListModels(ListOption{Search: "llama"})
			assert.ErrorIs(t, listErr, ErrUnauthorized)
		})
	}
}

// A 429 is retried, with backoff, and only for as long as somebody waiting on an
// HTTP handler will tolerate. Still throttled after that is a typed answer, not
// a generic failure.
func TestHuggingFace_RateLimitIsRetriedThenReported(t *testing.T) {
	client, requested := responderFor(http.StatusTooManyRequests, "rate limited", nil)
	hf := &huggingFace{url: "https://huggingface.co", client: client}

	_, err := hf.ListModels(ListOption{Search: "qwen"})
	assert.ErrorIs(t, err, ErrRateLimited)
	assert.Len(t, *requested, hubRetryAttempts, "the request is retried before giving up")
}

func TestHuggingFace_RateLimitRecoversOnRetry(t *testing.T) {
	attempts := 0
	client := &http.Client{Transport: &MockRoundTripper{
		RoundTripFunc: func(req *http.Request) (*http.Response, error) {
			attempts++

			if attempts == 1 {
				return &http.Response{
					StatusCode: http.StatusTooManyRequests,
					Body:       io.NopCloser(bytes.NewBufferString("slow down")),
					Header:     make(http.Header),
				}, nil
			}

			return &http.Response{
				StatusCode: http.StatusOK,
				Body:       io.NopCloser(bytes.NewBufferString("[]")),
				Header:     make(http.Header),
			}, nil
		},
	}}

	hf := &huggingFace{url: "https://huggingface.co", client: client}

	page, err := hf.ListModels(ListOption{Search: "qwen"})
	assert.NoError(t, err)
	assert.Empty(t, page.Models)
	assert.Equal(t, 2, attempts)
}

// The hub's own Retry-After wins over our backoff, capped: it is telling us what
// it wants, and the cap is because this is happening inside a request somebody
// is waiting on.
func TestHuggingFaceRetryDelay(t *testing.T) {
	withRetryAfter := func(value string) *http.Response {
		header := make(http.Header)
		if value != "" {
			header.Set("Retry-After", value)
		}

		return &http.Response{Header: header}
	}

	assert.Equal(t, time.Second, retryDelay(withRetryAfter("1"), 0))
	assert.Equal(t, hubRetryMaxDelay, retryDelay(withRetryAfter("60"), 0), "a long Retry-After is capped")
	assert.Equal(t, hubRetryBaseDelay, retryDelay(withRetryAfter(""), 0))
	assert.Equal(t, 2*hubRetryBaseDelay, retryDelay(withRetryAfter(""), 1), "backoff doubles")
	assert.Equal(t, hubRetryMaxDelay, retryDelay(withRetryAfter("not a number"), 9))
}
