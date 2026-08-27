package registry

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
)

func targetFor(server *httptest.Server, project string) RepositoryTarget {
	return RepositoryTarget{URL: server.URL, Project: project}
}

// harborServer answers the two endpoints a Harbor is asked for, and records
// what it was asked.
func harborServer(t *testing.T, repositories func(w http.ResponseWriter, r *http.Request)) (*httptest.Server, *[]url.Values) {
	t.Helper()

	asked := &[]url.Values{}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/api/v2.0/systeminfo":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"harbor_version":"v2.12.4","auth_mode":"ldap_auth"}`))
		case strings.HasSuffix(r.URL.Path, "/repositories"):
			*asked = append(*asked, r.URL.Query())
			repositories(w, r)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	return server, asked
}

func writeJSON(w http.ResponseWriter, body string) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(body))
}

func TestDetectListRepositoriesCapability(t *testing.T) {
	t.Run("recognises Docker Hub without asking it anything", func(t *testing.T) {
		// Its limitation is structural rather than a matter of permission, and
		// it is the one registry whose API is known ahead of time.
		for _, host := range []string{"docker.io", "https://index.docker.io", "registry-1.docker.io"} {
			capability, err := NewRepositoryService().
				DetectListRepositoriesCapability(RepositoryTarget{URL: host})

			require.NoError(t, err)
			require.Equalf(t, v1.ListRepositoriesNamespaceRequired, capability, "host %q", host)
		}
	})

	t.Run("recognises Harbor by what its body says, not by the status code", func(t *testing.T) {
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `[{"name":"team/x"}]`)
		})

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(targetFor(server, "team"))

		require.NoError(t, err)
		require.Equal(t, v1.ListRepositoriesHarborProjects, capability)
	})

	t.Run("does not take a proxy that answers 200 for everything as a Harbor", func(t *testing.T) {
		// This is the failure worth guarding: taking such a host as Harbor
		// leads to calls against endpoints that do not exist, and that failure
		// is very hard to trace back to the probe that caused it.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `{"status":"ok"}`)
		}))
		t.Cleanup(server.Close)

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(targetFor(server, "team"))

		require.NoError(t, err)
		require.Equal(t, v1.ListRepositoriesUnsupported, capability)
	})

	t.Run("does not take a non-JSON 200 as a Harbor either", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_, _ = w.Write([]byte("<html>hello</html>"))
		}))
		t.Cleanup(server.Close)

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(targetFor(server, "team"))

		require.NoError(t, err)
		require.Equal(t, v1.ListRepositoriesUnsupported, capability)
	})

	t.Run("reports a registry that is not a Harbor as unsupported", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(targetFor(server, "team"))

		require.NoError(t, err)
		require.Equal(t, v1.ListRepositoriesUnsupported, capability)
	})

	t.Run("separates a Harbor these credentials cannot read from one that is missing", func(t *testing.T) {
		// The registry can do it; this credential may not. A wider credential
		// fixes it, so it must not be recorded as unsupported.
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusForbidden)
		})

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(targetFor(server, "team"))

		require.NoError(t, err)
		require.Equal(t, v1.ListRepositoriesUnauthorized, capability)
	})

	t.Run("establishes nothing when the registry does not answer", func(t *testing.T) {
		// A timeout is not an answer about what a registry supports. Returning
		// an error rather than "unsupported" is what stops one bad minute from
		// disabling browsing until somebody notices.
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
		endpoint := server.URL
		server.Close()

		capability, err := NewRepositoryService().
			DetectListRepositoriesCapability(RepositoryTarget{URL: endpoint, Project: "team"})

		require.Error(t, err)
		require.Empty(t, capability)
	})
}

func TestListRepositoriesHarbor(t *testing.T) {
	t.Run("lets the server page and search", func(t *testing.T) {
		// Harbor does both, so neither is done here: a client-side filter over
		// one page would silently hide matches sitting on the next one.
		server, asked := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Total-Count", "120")
			writeJSON(w, `[{"name":"team/vllm"},{"name":"team/inner/x"}]`)
		})

		page, err := NewRepositoryService().ListRepositories(
			targetFor(server, "team"), v1.ListRepositoriesHarborProjects,
			RepositoryQuery{Search: "v", Page: 2, PageSize: 50})

		require.NoError(t, err)
		// Named relative to the registry's prefix, which is what the tags route
		// takes -- the project comes back off.
		require.Equal(t, []string{"vllm", "inner/x"}, page.Repositories)
		require.Equal(t, 120, page.Total)
		require.True(t, page.HasMore)

		require.Len(t, *asked, 1)
		require.Equal(t, "2", (*asked)[0].Get("page"))
		require.Equal(t, "50", (*asked)[0].Get("page_size"))
		require.Equal(t, "name=~v", (*asked)[0].Get("q"))
	})

	t.Run("reports the end of the listing from the total the server gave", func(t *testing.T) {
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			w.Header().Set("X-Total-Count", "2")
			writeJSON(w, `[{"name":"team/a"},{"name":"team/b"}]`)
		})

		page, err := NewRepositoryService().ListRepositories(
			targetFor(server, "team"), v1.ListRepositoriesHarborProjects,
			RepositoryQuery{Page: 1, PageSize: 2})

		require.NoError(t, err)
		require.False(t, page.HasMore)
	})

	t.Run("asks the project the registry is scoped to", func(t *testing.T) {
		var path string

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/api/v2.0/systeminfo" {
				writeJSON(w, `{"harbor_version":"v2.12.4"}`)
				return
			}

			path = r.URL.Path

			writeJSON(w, `[]`)
		}))
		t.Cleanup(server.Close)

		_, err := NewRepositoryService().ListRepositories(
			targetFor(server, "neutree-ai"), v1.ListRepositoriesHarborProjects, RepositoryQuery{})

		require.NoError(t, err)
		require.Equal(t, "/api/v2.0/projects/neutree-ai/repositories", path)
	})

	t.Run("refuses rather than guessing when the registry is scoped to nothing", func(t *testing.T) {
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `[]`)
		})

		_, err := NewRepositoryService().ListRepositories(
			targetFor(server, ""), v1.ListRepositoriesHarborProjects, RepositoryQuery{})

		require.ErrorIs(t, err, ErrNamespaceRequired)
	})

	t.Run("separates credentials that fall short from a project that is not there", func(t *testing.T) {
		for status, want := range map[int]error{
			http.StatusUnauthorized: ErrListRepositoriesUnauthorized,
			http.StatusForbidden:    ErrListRepositoriesUnauthorized,
			http.StatusNotFound:     ErrListRepositoriesUnsupported,
		} {
			server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(status)
			})

			_, err := NewRepositoryService().ListRepositories(
				targetFor(server, "team"), v1.ListRepositoriesHarborProjects, RepositoryQuery{})

			require.ErrorIsf(t, err, want, "status %d", status)
		}
	})
}

func TestListRepositoriesDockerHub(t *testing.T) {
	// hubService points the Docker Hub client at a stand-in, since its API host
	// is a different one from the registry that serves the images.
	hubService := func(server *httptest.Server) *repositoryService {
		svc := NewRepositoryService().(*repositoryService) //nolint:errcheck
		svc.hubAPI = server.URL

		return svc
	}

	t.Run("lists a namespace and names repositories as they are pulled", func(t *testing.T) {
		var asked url.Values

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			require.Equal(t, "/v2/repositories/vllm/", r.URL.Path)

			asked = r.URL.Query()

			writeJSON(w, `{"count":10,"next":"https://hub.docker.com/v2/repositories/vllm/?page=2",
				"results":[{"name":"vllm-openai"},{"name":"vllm-tpu"}]}`)
		}))
		t.Cleanup(server.Close)

		page, err := hubService(server).ListRepositories(
			RepositoryTarget{URL: "docker.io"}, v1.ListRepositoriesNamespaceRequired,
			RepositoryQuery{Namespace: "vllm", Page: 1, PageSize: 25})

		require.NoError(t, err)
		// A Docker Hub registry carries no project, so the namespace stays part
		// of the name -- exactly the reference a pull uses.
		require.Equal(t, []string{"vllm/vllm-openai", "vllm/vllm-tpu"}, page.Repositories)
		require.Equal(t, 10, page.Total)
		require.True(t, page.HasMore)
		require.Equal(t, "1", asked.Get("page"))
		require.Equal(t, "25", asked.Get("page_size"))
	})

	t.Run("takes the registry's own namespace off the name when it has one", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `{"count":1,"results":[{"name":"serve"}]}`)
		}))
		t.Cleanup(server.Close)

		page, err := hubService(server).ListRepositories(
			RepositoryTarget{URL: "docker.io", Project: "neutree"},
			v1.ListRepositoriesNamespaceRequired, RepositoryQuery{})

		require.NoError(t, err)
		require.Equal(t, []string{"serve"}, page.Repositories)
	})

	t.Run("asks for a namespace rather than pretending it can enumerate them", func(t *testing.T) {
		// Docker Hub has no endpoint that lists namespaces. Naming a built-in
		// set of them would be an inventory somebody has to maintain, which is
		// exactly what is avoided elsewhere.
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `{"count":0,"results":[]}`)
		}))
		t.Cleanup(server.Close)

		_, err := hubService(server).ListRepositories(
			RepositoryTarget{URL: "docker.io"}, v1.ListRepositoriesNamespaceRequired,
			RepositoryQuery{})

		require.ErrorIs(t, err, ErrNamespaceRequired)
	})

	t.Run("filters here, because Docker Hub does not", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `{"count":2,"results":[{"name":"vllm-openai"},{"name":"other"}]}`)
		}))
		t.Cleanup(server.Close)

		page, err := hubService(server).ListRepositories(
			RepositoryTarget{URL: "docker.io"}, v1.ListRepositoriesNamespaceRequired,
			RepositoryQuery{Namespace: "vllm", Search: "OPENAI"})

		require.NoError(t, err)
		require.Equal(t, []string{"vllm/vllm-openai"}, page.Repositories)
	})

	t.Run("reports a namespace that needs credentials as unauthorized", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusUnauthorized)
		}))
		t.Cleanup(server.Close)

		_, err := hubService(server).ListRepositories(
			RepositoryTarget{URL: "docker.io"}, v1.ListRepositoriesNamespaceRequired,
			RepositoryQuery{Namespace: "private"})

		require.ErrorIs(t, err, ErrListRepositoriesUnauthorized)
	})
}

func TestListRepositoriesHonoursTheStoredCapability(t *testing.T) {
	t.Run("refuses a registry recorded as unsupported without asking again", func(t *testing.T) {
		var asked int

		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			asked++

			w.WriteHeader(http.StatusNotFound)
		}))
		t.Cleanup(server.Close)

		_, err := NewRepositoryService().ListRepositories(
			targetFor(server, "team"), v1.ListRepositoriesUnsupported, RepositoryQuery{})

		require.ErrorIs(t, err, ErrListRepositoriesUnsupported)
		require.Zero(t, asked, "a recorded refusal answers on its own")
	})

	t.Run("probes when nothing has been recorded", func(t *testing.T) {
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `[{"name":"team/x"}]`)
		})

		page, err := NewRepositoryService().ListRepositories(
			targetFor(server, "team"), "", RepositoryQuery{})

		require.NoError(t, err)
		require.Equal(t, []string{"x"}, page.Repositories)
	})

	t.Run("re-probes a registry recorded as unauthorized", func(t *testing.T) {
		// The record is a cache of an observation, not a contract: a credential
		// widened since the last reconcile has to start working without waiting
		// for one.
		server, _ := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
			writeJSON(w, `[{"name":"team/x"}]`)
		})

		page, err := NewRepositoryService().ListRepositories(
			targetFor(server, "team"), v1.ListRepositoriesUnauthorized, RepositoryQuery{})

		require.NoError(t, err)
		require.Equal(t, []string{"x"}, page.Repositories)
	})
}

func TestRepositoryPagingBounds(t *testing.T) {
	server, asked := harborServer(t, func(w http.ResponseWriter, _ *http.Request) {
		writeJSON(w, `[]`)
	})

	svc := NewRepositoryService()

	for _, requested := range []int{0, -5, 100000} {
		_, err := svc.ListRepositories(targetFor(server, "team"),
			v1.ListRepositoriesHarborProjects, RepositoryQuery{PageSize: requested})
		require.NoError(t, err)
	}

	require.Len(t, *asked, 3)

	for i, want := range []string{
		fmt.Sprint(defaultRepositoryPageSize),
		fmt.Sprint(defaultRepositoryPageSize),
		fmt.Sprint(maxRepositoryPageSize),
	} {
		require.Equalf(t, want, (*asked)[i].Get("page_size"), "call %d", i)
	}
}
