package images

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	registryMocks "github.com/neutree-ai/neutree/internal/registry/mocks"
	storageMocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

// createTestContextWithQuery drives the handler the way the router does:
// workspace and registry from the path, paging and search from the query.
func createTestContextWithQuery(params map[string]string, query string) (*gin.Context, *httptest.ResponseRecorder) {
	gin.SetMode(gin.TestMode)

	w := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(w)
	c.Request = httptest.NewRequest(http.MethodGet, "/?"+query, nil)

	for k, v := range params {
		c.Params = append(c.Params, gin.Param{Key: k, Value: v})
	}

	return c, w
}

func repositoriesDeps(t *testing.T, stored *v1.ImageRegistry) (*Dependencies, *registryMocks.MockRepositoryService) {
	t.Helper()

	store := storageMocks.NewMockStorage(t)
	repositoryService := registryMocks.NewMockRepositoryService(t)
	store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{*stored}, nil)

	return &Dependencies{Storage: store, RepositoryService: repositoryService}, repositoryService
}

func harborRegistry(capability v1.ListRepositoriesCapability) *v1.ImageRegistry {
	return &v1.ImageRegistry{
		Spec: &v1.ImageRegistrySpec{URL: "https://registry.example.com", Repository: "neutree-ai"},
		Status: &v1.ImageRegistryStatus{
			Capabilities: &v1.ImageRegistryCapabilities{ListRepositories: capability},
		},
	}
}

func listRepositories(t *testing.T, deps *Dependencies, query string) (*httptest.ResponseRecorder, ImageRepositoriesResponse) {
	t.Helper()

	ctx, recorder := createTestContextWithQuery(map[string]string{
		"workspace": "default", "registry": "registry",
	}, query)
	listImageRepositories(deps)(ctx)

	var body ImageRepositoriesResponse
	if recorder.Code == http.StatusOK {
		assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))
	}

	return recorder, body
}

func failureBody(t *testing.T, recorder *httptest.ResponseRecorder) ImageRepositoriesErrorResponse {
	t.Helper()

	var body ImageRepositoriesErrorResponse
	assert.NoError(t, json.Unmarshal(recorder.Body.Bytes(), &body))

	return body
}

func TestListImageRepositories(t *testing.T) {
	t.Run("passes the paging and search the caller asked for", func(t *testing.T) {
		total := 120
		deps, repositoryService := repositoriesDeps(t, harborRegistry(v1.ListRepositoriesHarborProjects))
		repositoryService.
			On("ListRepositories", mock.Anything, v1.ListRepositoriesHarborProjects, registry.RepositoryQuery{
				Namespace: "", Search: "vllm", Page: 2, PageSize: 25,
			}).
			Return(registry.RepositoryPage{
				Repositories: []string{"neutree/vllm"}, Total: total, HasMore: true,
			}, nil)

		recorder, body := listRepositories(t, deps, "search=vllm&page=2&page_size=25")

		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, []string{"neutree/vllm"}, body.Repositories)
		assert.Equal(t, &total, body.Total)
		assert.True(t, body.HasMore)
		assert.Equal(t, v1.ListRepositoriesHarborProjects, body.Capability)
		repositoryService.AssertExpectations(t)
	})

	t.Run("passes the namespace through for a registry that needs one", func(t *testing.T) {
		deps, repositoryService := repositoriesDeps(t, &v1.ImageRegistry{
			Spec: &v1.ImageRegistrySpec{URL: "docker.io"},
			Status: &v1.ImageRegistryStatus{Capabilities: &v1.ImageRegistryCapabilities{
				ListRepositories: v1.ListRepositoriesNamespaceRequired,
			}},
		})
		repositoryService.
			On("ListRepositories", mock.Anything, v1.ListRepositoriesNamespaceRequired,
				mock.MatchedBy(func(q registry.RepositoryQuery) bool { return q.Namespace == "vllm" })).
			Return(registry.RepositoryPage{Repositories: []string{"vllm/vllm-openai"}, Total: -1}, nil)

		recorder, body := listRepositories(t, deps, "namespace=/vllm/")

		assert.Equal(t, http.StatusOK, recorder.Code)
		assert.Equal(t, []string{"vllm/vllm-openai"}, body.Repositories)
		assert.Nil(t, body.Total, "a registry that did not count says unknown, not zero")
		repositoryService.AssertExpectations(t)
	})

	t.Run("hands the stored capability over as the hint", func(t *testing.T) {
		// Established while connecting, so a user waiting on a list is not also
		// waiting on a probe.
		deps, repositoryService := repositoriesDeps(t, harborRegistry(""))
		repositoryService.
			On("ListRepositories", mock.Anything, v1.ListRepositoriesCapability(""), mock.Anything).
			Return(registry.RepositoryPage{Total: -1}, nil)

		recorder, _ := listRepositories(t, deps, "")

		assert.Equal(t, http.StatusOK, recorder.Code)
		repositoryService.AssertExpectations(t)
	})

	t.Run("asks for a namespace rather than reporting a failure", func(t *testing.T) {
		// Docker Hub has no endpoint that enumerates namespaces, so this is a
		// question to put to the user -- not something that went wrong.
		deps, repositoryService := repositoriesDeps(t, harborRegistry(v1.ListRepositoriesNamespaceRequired))
		repositoryService.
			On("ListRepositories", mock.Anything, mock.Anything, mock.Anything).
			Return(registry.RepositoryPage{}, errors.Wrap(registry.ErrNamespaceRequired, "docker.io"))

		recorder, _ := listRepositories(t, deps, "")

		body := failureBody(t, recorder)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Equal(t, reasonNamespaceRequired, body.Reason)
		assert.Contains(t, body.Message, "namespace")
	})

	t.Run("distinguishes a registry nothing here knows how to enumerate", func(t *testing.T) {
		// Permanent for this registry: the answer is to type the name, not to
		// try again.
		deps, repositoryService := repositoriesDeps(t, harborRegistry(v1.ListRepositoriesUnsupported))
		repositoryService.
			On("ListRepositories", mock.Anything, mock.Anything, mock.Anything).
			Return(registry.RepositoryPage{}, errors.Wrap(registry.ErrListRepositoriesUnsupported, "quay.io"))

		recorder, _ := listRepositories(t, deps, "")

		body := failureBody(t, recorder)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Equal(t, reasonNotSupported, body.Reason)
		assert.NotEmpty(t, body.Message, "the reason is branched on, the message is shown")
	})

	t.Run("distinguishes credentials that fall short", func(t *testing.T) {
		// A different sentence and a different action from the one above: this
		// registry can list, and someone has to issue a wider credential.
		deps, repositoryService := repositoriesDeps(t, harborRegistry(v1.ListRepositoriesUnauthorized))
		repositoryService.
			On("ListRepositories", mock.Anything, mock.Anything, mock.Anything).
			Return(registry.RepositoryPage{}, errors.Wrap(registry.ErrListRepositoriesUnauthorized, "registry.example.com"))

		recorder, _ := listRepositories(t, deps, "")

		body := failureBody(t, recorder)
		assert.Equal(t, http.StatusBadRequest, recorder.Code)
		assert.Equal(t, reasonRegistryUnauthorized, body.Reason)
		assert.Contains(t, body.Message, "not allowed")
	})

	t.Run("distinguishes a registry that could not be reached", func(t *testing.T) {
		// Says nothing about whether the listing is possible, so it is the one
		// failure worth retrying -- and the only one answered as 503.
		deps, repositoryService := repositoriesDeps(t, harborRegistry(v1.ListRepositoriesHarborProjects))
		repositoryService.
			On("ListRepositories", mock.Anything, mock.Anything, mock.Anything).
			Return(registry.RepositoryPage{}, errors.New("dial tcp: i/o timeout"))

		recorder, _ := listRepositories(t, deps, "")

		assert.Equal(t, http.StatusServiceUnavailable, recorder.Code)
		assert.Equal(t, reasonUnavailable, failureBody(t, recorder).Reason)
	})

	t.Run("rejects paging that is not a positive number", func(t *testing.T) {
		for _, query := range []string{"page=0", "page=-1", "page=many", "page_size=0", "page_size=x"} {
			ctx, recorder := createTestContextWithQuery(map[string]string{
				"workspace": "default", "registry": "registry",
			}, query)
			listImageRepositories(&Dependencies{})(ctx)

			assert.Equalf(t, http.StatusBadRequest, recorder.Code, "query %q", query)
		}
	})

	t.Run("reports an unknown registry as not found", func(t *testing.T) {
		store := storageMocks.NewMockStorage(t)
		store.On("ListImageRegistry", mock.Anything).Return([]v1.ImageRegistry{}, nil)

		ctx, recorder := createTestContextWithQuery(map[string]string{
			"workspace": "default", "registry": "absent",
		}, "")
		listImageRepositories(&Dependencies{Storage: store})(ctx)

		assert.Equal(t, http.StatusNotFound, recorder.Code)
	})
}
