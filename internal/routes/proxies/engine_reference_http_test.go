package proxies

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/golang-jwt/jwt/v4"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
)

func TestEngineReferenceHTTPStorageAuth(test *testing.T) {
	const callerToken = "Bearer engine-editor-without-endpoint-read"
	const signingKey = "engine-reference-http-test-signing-key"
	const removeFirstVersion = `{"spec":{"versions":[{"version":"v2.0.0"}]}}`
	const removeSecondVersion = `{"spec":{"versions":[{"version":"v1.0.0"}]}}`

	identitySelector := url.Values{
		"metadata->>name":      {"eq.custom-engine"},
		"metadata->>workspace": {"eq.workspace-a"},
	}
	logicalSelector := url.Values{
		"and": {"(metadata->>name.eq.custom-engine,metadata->>workspace.eq.workspace-a)"},
	}
	cases := []struct {
		name           string
		selector       url.Values
		body           string
		removedVersion string
		localReference bool
		wantStatus     int
	}{
		{
			name: "hidden endpoint blocks spec-only removal", selector: url.Values{"id": {"eq.7"}},
			body: removeFirstVersion, removedVersion: "v1.0.0", localReference: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "metadata selectors resolve stored identity", selector: identitySelector,
			body: removeFirstVersion, removedVersion: "v1.0.0", localReference: true, wantStatus: http.StatusBadRequest,
		},
		{
			name: "unsupported logical selector fails closed", selector: logicalSelector,
			body: removeFirstVersion,
		},
		{
			name: "other workspace engine and version references do not block", selector: identitySelector,
			body: removeFirstVersion, removedVersion: "v1.0.0", wantStatus: http.StatusNoContent,
		},
		{
			name: "unused version removal retains referenced version", selector: identitySelector,
			body: removeSecondVersion, removedVersion: "v2.0.0", localReference: true, wantStatus: http.StatusNoContent,
		},
		{
			name: "forced engine deletion checks all versions", selector: identitySelector,
			body:           `{"metadata":{"deletion_timestamp":"2026-10-20T00:00:00Z","annotations":{"neutree.ai/force-delete":"true"}}}`,
			localReference: true, wantStatus: http.StatusBadRequest,
		},
	}

	for _, testCase := range cases {
		test.Run(testCase.name, func(test *testing.T) {
			var engineReads, endpointCounts, callerLists atomic.Int32
			backend := httptest.NewServer(http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
				writer.Header().Set("Content-Type", "application/json")
				assert.Equal(test, http.MethodGet, request.Method)
				if request.URL.Path == "/endpoints" && request.Header.Get("Authorization") == callerToken {
					callerLists.Add(1)
					assert.Equal(test, "*", request.URL.Query().Get("select"))
					_, writeErr := io.WriteString(writer, "[]")
					assert.NoError(test, writeErr)
					return
				}

				token, parseErr := jwt.Parse(strings.TrimPrefix(request.Header.Get("Authorization"), "Bearer "),
					func(token *jwt.Token) (interface{}, error) { return []byte(signingKey), nil },
					jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
				if !assert.NoError(test, parseErr) || !assert.NotNil(test, token) {
					writer.WriteHeader(http.StatusUnauthorized)
					return
				}
				assert.True(test, token.Valid)
				claims, validClaims := token.Claims.(jwt.MapClaims)
				assert.True(test, validClaims)
				assert.Equal(test, "service_role", claims["role"])
				assert.NotEqual(test, callerToken, request.Header.Get("Authorization"))

				switch request.URL.Path {
				case "/engines":
					engineReads.Add(1)
					expected := url.Values{"select": {"*"}}
					for column, values := range testCase.selector {
						expected[column] = values
					}
					assert.Equal(test, expected, request.URL.Query())
					assert.NoError(test, json.NewEncoder(writer).Encode([]v1.Engine{engineReferenceFixture()}))
				case "/endpoints":
					endpointCounts.Add(1)
					expected := url.Values{
						"select": {"count"}, "metadata->>workspace": {"eq.workspace-a"},
						"spec->engine->>engine": {"eq.custom-engine"},
					}
					if testCase.removedVersion != "" {
						expected.Set("spec->engine->>version", "eq."+testCase.removedVersion)
					}
					assert.Equal(test, expected, request.URL.Query())
					references := []struct{ workspace, engine, version string }{
						{"workspace-b", "custom-engine", "v1.0.0"},
						{"workspace-a", "other-engine", "v1.0.0"},
						{"workspace-a", "custom-engine", "v3.0.0"},
					}
					if testCase.localReference {
						references = append(references, struct{ workspace, engine, version string }{"workspace-a", "custom-engine", "v1.0.0"})
					}
					count := 0
					query := request.URL.Query()
					for _, reference := range references {
						matchesVersion := query.Get("spec->engine->>version") == "" || query.Get("spec->engine->>version") == "eq."+reference.version
						if query.Get("metadata->>workspace") == "eq."+reference.workspace &&
							query.Get("spec->engine->>engine") == "eq."+reference.engine && matchesVersion {
							count++
						}
					}
					assert.NoError(test, json.NewEncoder(writer).Encode([]map[string]int{{"count": count}}))
				default:
					test.Errorf("unexpected fake PostgREST path: %s", request.URL.Path)
					writer.WriteHeader(http.StatusNotFound)
				}
			}))
			test.Cleanup(backend.Close)

			callerRequest, requestErr := http.NewRequest(http.MethodGet, backend.URL+"/endpoints?select=*", nil)
			require.NoError(test, requestErr)
			callerRequest.Header.Set("Authorization", callerToken)
			callerResponse, callerErr := backend.Client().Do(callerRequest)
			require.NoError(test, callerErr)
			callerBody, readErr := io.ReadAll(callerResponse.Body)
			require.NoError(test, callerResponse.Body.Close())
			require.NoError(test, readErr)
			require.Equal(test, http.StatusOK, callerResponse.StatusCode)
			require.JSONEq(test, "[]", string(callerBody))

			store, storageErr := storage.New(storage.Options{AccessURL: backend.URL, Scheme: "api", JwtSecret: signingKey})
			require.NoError(test, storageErr)
			response, forwarded := serveEngineProxyRequest(test, store, http.MethodPatch,
				"/engines?"+testCase.selector.Encode(), testCase.body)
			assert.EqualValues(test, 1, callerLists.Load())
			if testCase.selector.Has("and") {
				assert.Equal(test, http.StatusBadRequest, response.Code)
				assert.False(test, forwarded)
				assert.Zero(test, engineReads.Load())
				assert.Zero(test, endpointCounts.Load())
				assert.NotContains(test, response.Body.String(), "invalid Filter operator")
				return
			}

			assert.Equal(test, testCase.wantStatus, response.Code, response.Body.String())
			assert.Equal(test, testCase.wantStatus == http.StatusNoContent, forwarded)
			assert.EqualValues(test, 1, engineReads.Load())
			assert.EqualValues(test, 1, endpointCounts.Load())
			if testCase.wantStatus == http.StatusBadRequest {
				assert.Contains(test, response.Body.String(), `"code":"10133"`)
				for _, privateValue := range []string{"workspace-a", "workspace-b", "custom-engine", "v1.0.0", signingKey} {
					assert.NotContains(test, response.Body.String(), privateValue)
				}
			}
		})
	}
}
