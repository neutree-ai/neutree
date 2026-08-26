package images

import (
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"
	"github.com/pkg/errors"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/registry"
	"github.com/neutree-ai/neutree/pkg/storage"
)

// Why a repository listing did not come back. They are separated because what
// someone does about them differs completely: supply a namespace, obtain a
// wider credential, browse somewhere else, or try again. A client that only
// learns "the list is unavailable" can say none of that, and will let a user
// retry something that is never going to work.
//
// The words and the statuses are the model registry's
// (routes/models/errors.go) rather than new ones: a console already branches on
// that vocabulary, and a second spelling of the same ideas would be a second
// thing to learn.
const (
	// reasonNamespaceRequired -- the registry lists a namespace's repositories
	// and no namespace was given. Not a fault; the question to put to the user.
	reasonNamespaceRequired = "namespace_required"
	// reasonNotSupported -- nothing here knows how to enumerate this registry.
	reasonNotSupported = "not_supported"
	// reasonRegistryUnauthorized -- it would answer, and these credentials may
	// not. Someone has to issue a better one.
	reasonRegistryUnauthorized = "registry_unauthorized"
	// reasonUnavailable -- the registry could not be reached, or failed in a
	// way that says nothing about the others. Worth retrying.
	reasonUnavailable = "unavailable"
)

// ImageRepositoriesResponse lists the repositories an image registry holds.
type ImageRepositoriesResponse struct {
	// Repositories named relative to the registry's own repository prefix, so a
	// name here can be handed straight back to the tags route.
	Repositories []string `json:"repositories"`
	// Total matched, or null when the registry did not say.
	Total *int `json:"total"`
	// HasMore reports whether another page exists.
	HasMore bool `json:"has_more"`
	// Capability is what this registry turned out to be able to do. Echoed so a
	// client rendering the answer does not have to have read the registry
	// record to know which shape of listing it is looking at.
	Capability v1.ListRepositoriesCapability `json:"capability"`
}

// ImageRepositoriesErrorResponse explains why a listing is unavailable, shaped
// as the model registry's refusals are: prose to show, and a reason to branch
// on so a client never has to match on the prose.
type ImageRepositoriesErrorResponse struct {
	Message string `json:"message"`
	Reason  string `json:"reason"`
}

// listImageRepositories handles
// GET /workspaces/:workspace/image_registries/:registry/repositories.
//
// There is no portable way to enumerate a registry's repositories -- the OCI
// distribution spec defines no endpoint for it, and the /v2/_catalog inherited
// from Docker Registry v2 is refused by both registries that matter. So this
// asks each registry in its own dialect, guided by the capability the
// ImageRegistry controller established while connecting, and says plainly when
// there is no dialect to speak.
//
// Repositories come back named relative to the registry's own prefix, the same
// way getImageTags takes them, so browsing to a name and then asking for its
// tags needs no translation in between.
func listImageRepositories(deps *Dependencies) gin.HandlerFunc {
	return func(c *gin.Context) {
		workspace := c.Param("workspace")
		imageRegistryName := c.Param("registry")

		query, err := parseRepositoryQuery(c)
		if err != nil {
			c.JSON(http.StatusBadRequest, gin.H{"error": err.Error()})
			return
		}

		imageRegistries, err := deps.Storage.ListImageRegistry(storage.ListOption{
			Filters: []storage.Filter{
				{Column: "metadata->name", Operator: "eq", Value: fmt.Sprintf(`"%s"`, imageRegistryName)},
				{Column: "metadata->workspace", Operator: "eq", Value: fmt.Sprintf(`"%s"`, workspace)},
			},
		})
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to get image registry: %v", err)})
			return
		}

		if len(imageRegistries) == 0 {
			c.JSON(http.StatusNotFound, gin.H{"error": fmt.Sprintf("image registry %s/%s not found", workspace, imageRegistryName)})
			return
		}

		imageRegistry := &imageRegistries[0]

		target, err := registry.TargetFor(imageRegistry)
		if err != nil {
			c.JSON(http.StatusInternalServerError, gin.H{"error": fmt.Sprintf("failed to read image registry: %v", err)})
			return
		}

		capability := imageRegistry.GetListRepositoriesCapability()

		page, err := deps.RepositoryService.ListRepositories(target, capability, query)
		if err != nil {
			status, body := describeListFailure(err)
			c.JSON(status, body)

			return
		}

		var total *int
		if page.Total >= 0 {
			total = &page.Total
		}

		c.JSON(http.StatusOK, ImageRepositoriesResponse{
			Repositories: page.Repositories,
			Total:        total,
			HasMore:      page.HasMore,
			Capability:   capability,
		})
	}
}

// describeListFailure turns a listing failure into a status and a reason,
// following what the caller can do about it the way respondRegistryError does:
// 400 when the registry as configured cannot serve the request, 503 when the
// request was not wrong and coming back later is the answer.
func describeListFailure(err error) (int, ImageRepositoriesErrorResponse) {
	switch {
	case errors.Is(err, registry.ErrNamespaceRequired):
		return http.StatusBadRequest, ImageRepositoriesErrorResponse{
			Message: "a namespace is required to list repositories in this registry: " +
				"Docker Hub has no endpoint that enumerates namespaces, so one has to be named",
			Reason: reasonNamespaceRequired,
		}
	case errors.Is(err, registry.ErrListRepositoriesUnsupported):
		return http.StatusBadRequest, ImageRepositoriesErrorResponse{
			Message: fmt.Sprintf("this image registry cannot list repositories: %v", err),
			Reason:  reasonNotSupported,
		}
	case errors.Is(err, registry.ErrListRepositoriesUnauthorized):
		return http.StatusBadRequest, ImageRepositoriesErrorResponse{
			Message: fmt.Sprintf("the credentials stored for this image registry are not allowed to "+
				"list its repositories: %v", err),
			Reason: reasonRegistryUnauthorized,
		}
	default:
		klog.Warningf("failed to list repositories: %v", err)

		return http.StatusServiceUnavailable, ImageRepositoriesErrorResponse{
			Message: fmt.Sprintf("failed to reach the image registry to list repositories: %v", err),
			Reason:  reasonUnavailable,
		}
	}
}

func parseRepositoryQuery(c *gin.Context) (registry.RepositoryQuery, error) {
	page, err := parsePositive(c.Query("page"), 1)
	if err != nil {
		return registry.RepositoryQuery{}, errors.Wrap(err, "page")
	}

	pageSize, err := parsePositive(c.Query("page_size"), 0)
	if err != nil {
		return registry.RepositoryQuery{}, errors.Wrap(err, "page_size")
	}

	return registry.RepositoryQuery{
		Namespace: strings.Trim(strings.TrimSpace(c.Query("namespace")), "/"),
		Search:    strings.TrimSpace(c.Query("search")),
		Page:      page,
		PageSize:  pageSize,
	}, nil
}

func parsePositive(raw string, fallback int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return fallback, nil
	}

	value, err := strconv.Atoi(raw)
	if err != nil || value < 1 {
		return 0, errors.Errorf("must be a positive integer, got %q", raw)
	}

	return value, nil
}
