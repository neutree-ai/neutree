package registry

import (
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

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

// Listing the repositories a registry holds has no portable answer.
//
// The OCI distribution spec defines no endpoint for it. Its only listing is
// end-8a/8b, GET /v2/<name>/tags/list, which already needs the repository name.
// The /v2/_catalog that registries inherited from Docker Registry v2 is outside
// the spec and, where it exists, is refused: Docker Hub's token service issues
// no `registry:catalog:*` scope to anybody, and Harbor gates it on system
// administrator, which is not a credential anyone should be storing here to get
// a dropdown.
//
// So each registry is asked in the dialect it actually answers:
//
//	Harbor       GET /api/v2.0/projects/{project}/repositories?page=&page_size=&q=
//	             -- paged and searched by the server, open to a credential that
//	             can read the project.
//	Docker Hub   GET https://hub.docker.com/v2/repositories/{namespace}/?page=&page_size=
//	             -- open to nobody in particular for a public namespace. There is
//	             no endpoint that enumerates namespaces, so the namespace has to
//	             come from the user.
//
// Anything else is reported as unsupported rather than guessed at.

const (
	// harborSystemInfoPath is the probe. It is unauthenticated on every Harbor
	// deployment, which is what makes it usable before credentials are known to
	// work.
	harborSystemInfoPath = "/api/v2.0/systeminfo"
	// dockerHubAPI is Docker Hub's own API, which is a different host from the
	// registry that serves the images.
	dockerHubAPI = "https://hub.docker.com"

	// probeTimeout bounds a capability probe. It is deliberately generous: a
	// slow link is not an answer about what a registry supports, and marking a
	// working registry unsupported because of one bad minute is far worse than
	// waiting.
	probeTimeout = 20 * time.Second
	// listTimeout bounds a listing, which a user is waiting on.
	listTimeout = 30 * time.Second

	defaultRepositoryPageSize = 50
	maxRepositoryPageSize     = 100
)

// ErrNamespaceRequired is returned when a registry can only list a namespace's
// repositories and no namespace was given. It is not a failure -- it is the
// question the caller has to put to the user.
var ErrNamespaceRequired = errors.New("a namespace is required to list repositories in this registry")

// ErrListRepositoriesUnsupported is returned when nothing here knows how to
// enumerate this registry's repositories.
var ErrListRepositoriesUnsupported = errors.New("this registry does not support listing repositories")

// ErrListRepositoriesUnauthorized is returned when the registry would answer
// but these credentials may not. A wider credential fixes it; retrying does not.
var ErrListRepositoriesUnauthorized = errors.New("these credentials are not allowed to list this registry's repositories")

// RepositoryTarget is one registry, reduced to what listing it needs.
type RepositoryTarget struct {
	// URL as configured, scheme included when there is one.
	URL string
	// Project is the registry's own repository prefix: a Harbor project, or a
	// Docker Hub namespace.
	Project  string
	Username string
	Password string
}

// TargetFor reduces a stored registry to what this service needs.
func TargetFor(imageRegistry *v1.ImageRegistry) (RepositoryTarget, error) {
	if imageRegistry == nil || imageRegistry.Spec == nil {
		return RepositoryTarget{}, errors.New("image registry spec is nil")
	}

	username, password, err := util.GetImageRegistryAuthInfo(imageRegistry)
	if err != nil {
		return RepositoryTarget{}, errors.Wrap(err, "failed to get auth info")
	}

	return RepositoryTarget{
		URL:      imageRegistry.Spec.URL,
		Project:  strings.Trim(strings.TrimSpace(imageRegistry.Spec.Repository), "/"),
		Username: username,
		Password: password,
	}, nil
}

// RepositoryQuery is one page of a listing.
type RepositoryQuery struct {
	// Namespace to list. Ignored by Harbor, which lists the project the
	// registry is scoped to; required by Docker Hub.
	Namespace string
	// Search narrows the listing. Applied by the server where the registry
	// supports it.
	Search   string
	Page     int
	PageSize int
}

// RepositoryPage is what came back.
type RepositoryPage struct {
	// Repositories named relative to the registry's own prefix, which is the
	// form the tags route takes.
	Repositories []string
	// Total matched, or -1 when the registry did not say.
	Total int
	// HasMore reports whether another page exists.
	HasMore bool
}

// RepositoryService enumerates what a registry holds, in whichever dialect it
// speaks.
type RepositoryService interface {
	// DetectListRepositoriesCapability establishes how this registry can be
	// enumerated.
	//
	// It returns an error, and no capability, when nothing was established --
	// a timeout, a refused connection. A caller must not record that as
	// unsupported: the registry has said nothing, and a bad minute would
	// otherwise disable browsing until someone noticed.
	DetectListRepositoriesCapability(target RepositoryTarget) (v1.ListRepositoriesCapability, error)
	// ListRepositories lists one page, using the capability as a hint about
	// which dialect to speak. An empty hint makes it probe first.
	ListRepositories(target RepositoryTarget, hint v1.ListRepositoriesCapability,
		query RepositoryQuery) (RepositoryPage, error)
}

type repositoryService struct {
	client *http.Client
	// hubAPI is Docker Hub's API root. A field rather than the constant so a
	// test can stand in for a host this process should not be reaching.
	hubAPI string
}

func NewRepositoryService() RepositoryService {
	return &repositoryService{
		hubAPI: dockerHubAPI,
		client: &http.Client{
			Timeout: listTimeout,
			Transport: &http.Transport{
				TLSClientConfig: &tls.Config{InsecureSkipVerify: true}, //nolint:gosec
			},
		},
	}
}

func (svc *repositoryService) DetectListRepositoriesCapability(
	target RepositoryTarget) (v1.ListRepositoriesCapability, error) {
	if isDockerHubTarget(target.URL) {
		// Docker Hub needs no probe: it is the one registry whose API is known
		// ahead of time, and whose limitation is structural rather than a
		// matter of permission.
		return v1.ListRepositoriesNamespaceRequired, nil
	}

	isHarbor, err := svc.probeHarbor(target)
	if err != nil {
		return "", err
	}

	if !isHarbor {
		return v1.ListRepositoriesUnsupported, nil
	}

	// Harbor speaks the dialect; whether these credentials may use it is a
	// separate question, and one worth answering now rather than when a user is
	// waiting on a list.
	_, err = svc.listHarbor(target, RepositoryQuery{PageSize: 1})
	if err == nil {
		return v1.ListRepositoriesHarborProjects, nil
	}

	if errors.Is(err, ErrListRepositoriesUnauthorized) {
		return v1.ListRepositoriesUnauthorized, nil
	}

	// A Harbor whose project is absent, or which is scoped to no project at
	// all, cannot be enumerated as configured. That is an answer; only a
	// registry that said nothing gets to leave the capability unestablished.
	if errors.Is(err, ErrListRepositoriesUnsupported) || errors.Is(err, ErrNamespaceRequired) {
		return v1.ListRepositoriesUnsupported, nil
	}

	return "", err
}

// probeHarbor reports whether this is a Harbor.
//
// The body is what decides it, not the status code. A reverse proxy that
// answers 200 for every path is common enough, and taking that as a Harbor
// leads to calls against endpoints that do not exist -- a failure that is
// extremely hard to trace back to here. Checked against the two registries
// nearest to hand: quay.io answers 308 for this path, registry-1.docker.io 404.
func (svc *repositoryService) probeHarbor(target RepositoryTarget) (bool, error) {
	endpoint, err := harborBaseURL(target.URL)
	if err != nil {
		return false, err
	}

	res, err := svc.do(http.MethodGet, endpoint+harborSystemInfoPath, target, probeTimeout)
	if err != nil {
		return false, err
	}

	defer res.body.Close()

	if res.status != http.StatusOK {
		return false, nil
	}

	var info struct {
		HarborVersion string `json:"harbor_version"`
	}

	if err := json.NewDecoder(io.LimitReader(res.body, 1<<20)).Decode(&info); err != nil {
		// Something answered 200 with a body that is not Harbor's. That is an
		// answer -- it is not a Harbor -- rather than a failure.
		return false, nil
	}

	return info.HarborVersion != "", nil
}

func (svc *repositoryService) ListRepositories(target RepositoryTarget, hint v1.ListRepositoriesCapability,
	query RepositoryQuery) (RepositoryPage, error) {
	capability := hint

	if capability == "" || capability == v1.ListRepositoriesUnauthorized {
		// No hint, or a hint recorded before the credentials were widened. The
		// stored capability is a cache of an observation, so it is never the
		// last word on whether a call works.
		detected, err := svc.DetectListRepositoriesCapability(target)
		if err != nil {
			return RepositoryPage{}, err
		}

		capability = detected
	}

	switch capability {
	case v1.ListRepositoriesHarborProjects:
		return svc.listHarbor(target, query)
	case v1.ListRepositoriesNamespaceRequired:
		return svc.listDockerHub(target, query)
	case v1.ListRepositoriesUnauthorized:
		return RepositoryPage{}, ErrListRepositoriesUnauthorized
	case v1.ListRepositoriesUnsupported:
		return RepositoryPage{}, ErrListRepositoriesUnsupported
	default:
		return RepositoryPage{}, ErrListRepositoriesUnsupported
	}
}

// listHarbor lists a project's repositories. Harbor pages and searches on the
// server, so neither is done here.
func (svc *repositoryService) listHarbor(target RepositoryTarget, query RepositoryQuery) (RepositoryPage, error) {
	project := target.Project
	if query.Namespace != "" {
		project = query.Namespace
	}

	if project == "" {
		// A Harbor URL with no project is a registry scoped to nothing, and
		// Harbor has no endpoint that lists across projects for a project-level
		// credential.
		return RepositoryPage{}, ErrNamespaceRequired
	}

	endpoint, err := harborBaseURL(target.URL)
	if err != nil {
		return RepositoryPage{}, err
	}

	params := url.Values{}
	params.Set("page", strconv.Itoa(pageOf(query)))
	params.Set("page_size", strconv.Itoa(pageSizeOf(query)))

	if query.Search != "" {
		// Harbor's generic query syntax: ~ is a substring match.
		params.Set("q", fmt.Sprintf("name=~%s", query.Search))
	}

	res, err := svc.do(http.MethodGet, fmt.Sprintf("%s/api/v2.0/projects/%s/repositories?%s",
		endpoint, url.PathEscape(project), params.Encode()), target, listTimeout)
	if err != nil {
		return RepositoryPage{}, err
	}

	defer res.body.Close()

	if err := checkListStatus(res.status); err != nil {
		return RepositoryPage{}, err
	}

	var items []struct {
		// Harbor names a repository with its project in front. Relative to the
		// registry's prefix, which is what the tags route takes, that prefix
		// comes back off.
		Name string `json:"name"`
	}

	if err := json.NewDecoder(io.LimitReader(res.body, 8<<20)).Decode(&items); err != nil {
		return RepositoryPage{}, errors.Wrap(err, "failed to read the repositories Harbor listed")
	}

	repositories := make([]string, 0, len(items))

	for _, item := range items {
		name := strings.TrimPrefix(strings.Trim(item.Name, "/"), project+"/")
		if name != "" {
			repositories = append(repositories, name)
		}
	}

	total := -1

	if header := res.header.Get("X-Total-Count"); header != "" {
		if parsed, convErr := strconv.Atoi(header); convErr == nil {
			total = parsed
		}
	}

	return RepositoryPage{
		Repositories: repositories,
		Total:        total,
		HasMore:      hasMore(total, pageOf(query), pageSizeOf(query), len(items)),
	}, nil
}

// listDockerHub lists a namespace's repositories.
//
// Docker Hub has no endpoint that enumerates namespaces, so one has to be
// given. That is the one place in this feature where the user must type rather
// than choose, and it is not worked around: a built-in list of namespaces would
// be an inventory somebody has to maintain, which is exactly what is avoided
// elsewhere.
func (svc *repositoryService) listDockerHub(target RepositoryTarget, query RepositoryQuery) (RepositoryPage, error) {
	namespace := query.Namespace
	if namespace == "" {
		namespace = target.Project
	}

	if namespace == "" {
		return RepositoryPage{}, ErrNamespaceRequired
	}

	params := url.Values{}
	params.Set("page", strconv.Itoa(pageOf(query)))
	params.Set("page_size", strconv.Itoa(pageSizeOf(query)))

	res, err := svc.do(http.MethodGet, fmt.Sprintf("%s/v2/repositories/%s/?%s",
		svc.hubAPI, url.PathEscape(namespace), params.Encode()), target, listTimeout)
	if err != nil {
		return RepositoryPage{}, err
	}

	defer res.body.Close()

	if err := checkListStatus(res.status); err != nil {
		return RepositoryPage{}, err
	}

	var page struct {
		Count   int    `json:"count"`
		Next    string `json:"next"`
		Results []struct {
			Name string `json:"name"`
		} `json:"results"`
	}

	if err := json.NewDecoder(io.LimitReader(res.body, 8<<20)).Decode(&page); err != nil {
		return RepositoryPage{}, errors.Wrap(err, "failed to read the repositories Docker Hub listed")
	}

	repositories := make([]string, 0, len(page.Results))

	for _, item := range page.Results {
		if item.Name == "" {
			continue
		}

		// Named relative to the registry's own prefix, which is what the tags
		// route takes. A Docker Hub registry usually carries no project, so the
		// namespace stays part of the name -- exactly as it is pulled.
		name := namespace + "/" + item.Name
		if target.Project != "" {
			name = strings.TrimPrefix(name, target.Project+"/")
		}

		// Docker Hub does not filter, so the search is applied here.
		if !matchesSearch(name, query.Search) {
			continue
		}

		repositories = append(repositories, name)
	}

	return RepositoryPage{
		Repositories: repositories,
		Total:        page.Count,
		HasMore:      page.Next != "",
	}, nil
}

type response struct {
	status int
	header http.Header
	body   io.ReadCloser
}

func (svc *repositoryService) do(method, endpoint string, target RepositoryTarget,
	timeout time.Duration) (*response, error) {
	request, err := http.NewRequest(method, endpoint, nil)
	if err != nil {
		return nil, errors.Wrap(err, "failed to build a request for "+endpoint)
	}

	request.Header.Set("Accept", "application/json")

	// Anonymous where there is nothing to send: an empty Authorization header
	// makes some registries answer 401 for something they would have served.
	if target.Username != "" || target.Password != "" {
		request.SetBasicAuth(target.Username, target.Password)
	}

	client := *svc.client
	client.Timeout = timeout

	// The body is handed to the caller, which closes it; bodyclose cannot see
	// across the return.
	res, err := client.Do(request) //nolint:bodyclose
	if err != nil {
		// Nothing was established. Deliberately not classified: a link that
		// dropped says nothing about what the registry supports.
		return nil, errors.Wrap(err, "failed to reach "+endpoint)
	}

	return &response{status: res.StatusCode, header: res.Header, body: res.Body}, nil
}

func checkListStatus(status int) error {
	switch {
	case status == http.StatusUnauthorized, status == http.StatusForbidden:
		return ErrListRepositoriesUnauthorized
	case status == http.StatusNotFound:
		// The project or namespace is not there, or is not visible to these
		// credentials. Either way there is nothing to list under it.
		return ErrListRepositoriesUnsupported
	case status >= http.StatusBadRequest:
		return errors.Errorf("the registry refused to list repositories: %d %s",
			status, http.StatusText(status))
	default:
		return nil
	}
}

func matchesSearch(name, search string) bool {
	if search == "" {
		return true
	}

	return strings.Contains(strings.ToLower(name), strings.ToLower(search))
}

func pageOf(query RepositoryQuery) int {
	if query.Page < 1 {
		return 1
	}

	return query.Page
}

func pageSizeOf(query RepositoryQuery) int {
	if query.PageSize < 1 {
		return defaultRepositoryPageSize
	}

	if query.PageSize > maxRepositoryPageSize {
		return maxRepositoryPageSize
	}

	return query.PageSize
}

// hasMore prefers the total the server reported, and falls back to "the page
// came back full" when it reported none.
func hasMore(total, page, pageSize, returned int) bool {
	if total >= 0 {
		return page*pageSize < total
	}

	return returned >= pageSize
}

// isDockerHubTarget reports whether a registry URL is Docker Hub, reusing the
// host set the pull side already recognises.
func isDockerHubTarget(rawURL string) bool {
	return util.IsDockerHubImagePrefix(util.StripRegistryScheme(rawURL))
}

// harborBaseURL turns a configured registry URL into something to make API
// calls against, supplying the scheme the configuration leaves out.
func harborBaseURL(rawURL string) (string, error) {
	host := util.StripRegistryScheme(rawURL)
	if host == "" {
		return "", errors.New("image registry url is empty")
	}

	scheme := "https"
	if util.IsHTTPRegistryURL(rawURL) {
		scheme = "http"
	}

	// A URL carrying a project path is still addressed at its host: Harbor's
	// API lives at the root, not under the project.
	host = strings.SplitN(host, "/", 2)[0]

	return scheme + "://" + host, nil
}
