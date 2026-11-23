package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/pkg/storage"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func TestBuildNeutreeACLGroup(t *testing.T) {
	group := BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a")

	assert.True(t, strings.HasPrefix(group, NeutreeACLGroupPrefix))
	assert.True(t, strings.HasPrefix(group, "nt:endpoint:"))
	assert.Equal(t, group, BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a"))
	assert.NotEqual(t, group, BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-b"))
	assert.NotEqual(t, group, BuildNeutreeACLGroup("workspace-a", ACLResourceExternalEndpoint, "chat-a"))
}

func TestBuildNeutreeACLGroupSpecialCharacters(t *testing.T) {
	group := BuildNeutreeACLGroup("team/a:b", ACLResourceExternalEndpoint, "open ai/prod")

	assert.True(t, strings.HasPrefix(group, "nt:external-endpoint:"))
	assert.NotContains(t, strings.TrimPrefix(group, "nt:external-endpoint:"), "/")
	assert.NotContains(t, strings.TrimPrefix(group, "nt:external-endpoint:"), " ")
	assert.LessOrEqual(t, len(group), 90)
}

func TestIsNeutreeACLGroup(t *testing.T) {
	assert.True(t, IsNeutreeACLGroup("nt:endpoint:abc123"))
	assert.True(t, IsNeutreeACLGroup("nt:external-endpoint:abc123"))
	assert.False(t, IsNeutreeACLGroup("manual-group"))
	assert.False(t, IsNeutreeACLGroup(""))
}

func TestDesiredAPIKeyACLGroups(t *testing.T) {
	cases := []struct {
		name      string
		canInvoke bool
		want      []string
	}{
		{
			name:      "model:invoke reaches every endpoint and external endpoint",
			canInvoke: true,
			want: sortedGroups(
				BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a"),
				BuildNeutreeACLGroup("workspace-a", ACLResourceExternalEndpoint, "ee-a"),
			),
		},
		{
			// endpoint:read alone (not asked for any more) reaches nothing.
			name:      "without model:invoke the key reaches nothing",
			canInvoke: false,
			want:      []string{},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			apiKey := &v1.ApiKey{
				ID:       "key-1",
				UserID:   "user-1",
				Metadata: &v1.Metadata{Workspace: "workspace-a"},
			}

			s := storagemocks.NewMockStorage(t)
			k := &Kong{storage: s}

			expectPermission(s, "user-1", "workspace-a", "model:invoke", tc.canInvoke)

			if tc.canInvoke {
				s.On("ListEndpoint", workspaceListOption("workspace-a")).Return([]v1.Endpoint{
					{Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"}},
					{Metadata: &v1.Metadata{Name: "deleted", Workspace: "workspace-a", DeletionTimestamp: "2026-01-01T00:00:00Z"}},
				}, nil).Once()
				s.On("ListExternalEndpoint", workspaceListOption("workspace-a")).Return([]v1.ExternalEndpoint{
					{Metadata: &v1.Metadata{Name: "ee-a", Workspace: "workspace-a"}},
				}, nil).Once()
			}

			groups, err := k.desiredAPIKeyACLGroups(apiKey)

			assert.NoError(t, err)
			assert.Equal(t, tc.want, groups)
		})
	}
}

func sortedGroups(groups ...string) []string {
	sort.Strings(groups)

	return groups
}

func TestDesiredAPIKeyACLGroupsNoWorkspaceOrUser(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	k := &Kong{storage: s}

	groups, err := k.desiredAPIKeyACLGroups(&v1.ApiKey{})

	assert.NoError(t, err)
	assert.Empty(t, groups)
}

func expectPermission(s *storagemocks.MockStorage, userID, workspace, permission string, allowed bool) {
	s.On("CallDatabaseFunction", "has_permission", map[string]interface{}{
		"user_uuid":           userID,
		"required_permission": permission,
		"workspace":           workspace,
	}, mock.Anything).Run(func(args mock.Arguments) {
		result := args.Get(2).(*bool)
		*result = allowed
	}).Return(nil).Once()
}

func workspaceListOption(workspace string) interface{} {
	return mock.MatchedBy(func(option storage.ListOption) bool {
		return len(option.Filters) == 1 &&
			option.Filters[0].Column == "metadata->workspace" &&
			option.Filters[0].Operator == "eq" &&
			option.Filters[0].Value == strconv.Quote(workspace)
	})
}

func TestDiffNeutreeACLGroups(t *testing.T) {
	current := []*kong.ACLGroup{
		{ID: pointy.String("acl-1"), Group: pointy.String("nt:endpoint:old")},
		{ID: pointy.String("acl-2"), Group: pointy.String("nt:endpoint:keep")},
		{ID: pointy.String("acl-3"), Group: pointy.String("manual-group")},
	}
	desired := []string{"nt:endpoint:keep", "nt:endpoint:new"}

	toCreate, toDelete := diffNeutreeACLGroups(current, desired)

	assert.Equal(t, []string{"nt:endpoint:new"}, toCreate)
	assert.Len(t, toDelete, 1)
	assert.Equal(t, "nt:endpoint:old", *toDelete[0].Group)
}

func TestSyncAPIKeyRequiresStatusSkValue(t *testing.T) {
	k := &Kong{}

	err := k.SyncAPIKey(&v1.ApiKey{ID: "key-without-status"})

	assert.Error(t, err)
	assert.Contains(t, err.Error(), "api key status.sk_value is required")
}

func TestSyncAPIKeyACLGroupsDiffsConsumerGroups(t *testing.T) {
	apiKey := &v1.ApiKey{
		ID:     "key-1",
		UserID: "user-1",
		Metadata: &v1.Metadata{
			Workspace: "workspace-a",
		},
	}
	desiredGroup := BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a")
	staleGroup := BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "stale")

	var createdGroups []string
	var deletedPaths []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/acls":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[{"id":"stale-id","group":"` + staleGroup + `"},{"id":"manual-id","group":"manual-group"}]}`))
		case r.Method == http.MethodPost && r.URL.Path == "/consumers/consumer-1/acls":
			var body kong.ACLGroup
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.NotNil(t, body.Group)
			createdGroups = append(createdGroups, *body.Group)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"created-id","group":"` + *body.Group + `"}`))
		case r.Method == http.MethodDelete:
			deletedPaths = append(deletedPaths, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/plugins/"):
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/plugins":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"plugin-1"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/plugins":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[]}`))
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	expectPermission(s, "user-1", "workspace-a", "model:invoke", true)
	s.On("ListEndpoint", workspaceListOption("workspace-a")).Return([]v1.Endpoint{
		{Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"}},
	}, nil).Once()
	s.On("ListExternalEndpoint", workspaceListOption("workspace-a")).Return([]v1.ExternalEndpoint{}, nil).Once()

	k := &Kong{kongClient: client, storage: s}

	err = k.syncAPIKeyGatewayConfig(pointy.String("consumer-1"), apiKey, modelRules{})

	require.NoError(t, err)
	assert.Equal(t, []string{desiredGroup}, createdGroups)
	assert.Equal(t, []string{"/consumers/consumer-1/acls/" + staleGroup}, deletedPaths)
}

func TestGenerateEndpointACLPlugin(t *testing.T) {
	k := &Kong{}
	route := &kong.Route{ID: pointy.String("route-1")}
	ep := &v1.Endpoint{
		Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"},
	}

	plugin := k.generateEndpointACLPlugin(ep, route)

	assert.Equal(t, "acl", *plugin.Name)
	assert.True(t, strings.HasPrefix(*plugin.InstanceName, "neutree-acl-"))
	assert.Equal(t, route, plugin.Route)
	assert.Equal(t, []string{
		BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a"),
	}, plugin.Config["allow"])
	assert.Equal(t, true, plugin.Config["hide_groups_header"])
}

func TestGenerateExternalEndpointACLPlugin(t *testing.T) {
	k := &Kong{}
	route := &kong.Route{ID: pointy.String("route-2")}
	ee := &v1.ExternalEndpoint{
		Metadata: &v1.Metadata{Name: "ee-a", Workspace: "workspace-a"},
	}

	plugin := k.generateExternalEndpointACLPlugin(ee, route)

	assert.Equal(t, "acl", *plugin.Name)
	assert.True(t, strings.HasPrefix(*plugin.InstanceName, "neutree-acl-"))
	assert.Equal(t, route, plugin.Route)
	assert.Equal(t, []string{
		BuildNeutreeACLGroup("workspace-a", ACLResourceExternalEndpoint, "ee-a"),
	}, plugin.Config["allow"])
	assert.Equal(t, true, plugin.Config["hide_groups_header"])
}

func expectOwnerActive(s *storagemocks.MockStorage, keyID string, active bool) {
	s.On("ApiKeyOwnerActive", keyID).Return(active, nil).Once()
}

// A key whose owner is deactivated loses its credential and ACL groups; the
// consumer itself stays, and no group rules or permissions are read.
func TestSyncAPIKeySuspendsKeyOfDeactivatedOwner(t *testing.T) {
	endpointGroup := BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a")

	var deleted []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/consumers" && r.URL.Query().Get("custom_id") == "key-1":
			_, _ = w.Write([]byte(`{"data":[{"id":"consumer-1","custom_id":"key-1"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/key-auth":
			_, _ = w.Write([]byte(`{"data":[{"id":"ka-1","key":"sk-1"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/acls":
			_, _ = w.Write([]byte(`{"data":[{"id":"acl-1","group":"` + endpointGroup + `"},{"id":"acl-2","group":"manual-group"}]}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.String())
		}
	}))
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	expectOwnerActive(s, "key-1", false)

	err = (&Kong{kongClient: client, storage: s}).SyncAPIKey(&v1.ApiKey{
		ID:       "key-1",
		UserID:   "user-1",
		Metadata: &v1.Metadata{Workspace: "workspace-a"},
		Status:   &v1.ApiKeyStatus{SkValue: "sk-1"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{
		"/consumers/consumer-1/key-auth/ka-1",
		"/consumers/consumer-1/acls/" + endpointGroup,
	}, deleted)
}

// A ServiceAccount key is decided by the ServiceAccount's own model:invoke.
func TestDesiredAPIKeyACLGroupsServiceAccountKey(t *testing.T) {
	s := storagemocks.NewMockStorage(t)
	expectPermission(s, "sa-1", "workspace-a", "model:invoke", true)
	s.On("ListEndpoint", workspaceListOption("workspace-a")).Return([]v1.Endpoint{
		{Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"}},
	}, nil).Once()
	s.On("ListExternalEndpoint", workspaceListOption("workspace-a")).Return([]v1.ExternalEndpoint{}, nil).Once()

	groups, err := (&Kong{storage: s}).desiredAPIKeyACLGroups(&v1.ApiKey{
		ID:               "sa-key",
		ServiceAccountID: "sa-1",
		Metadata:         &v1.Metadata{Workspace: "workspace-a"},
	})

	require.NoError(t, err)
	assert.Equal(t, []string{BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a")}, groups)
}

// syncOrderServer is a Kong stub that records each write as "METHOD path".
// The consumer has key-auth sk-1; currentACLs and currentPlugins are its
// existing ACL groups and plugins.
func syncOrderServer(t *testing.T, currentACLs, currentPlugins string) (*httptest.Server, *[]string) {
	var writes []string

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")

		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/consumers" && r.URL.Query().Get("custom_id") == "key-1":
			_, _ = w.Write([]byte(`{"data":[{"id":"consumer-1","custom_id":"key-1"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/key-auth":
			_, _ = w.Write([]byte(`{"data":[{"id":"ka-1","key":"sk-1"}]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/acls":
			_, _ = w.Write([]byte(`{"data":[` + currentACLs + `]}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/plugins":
			_, _ = w.Write([]byte(`{"data":[` + currentPlugins + `]}`))
		case r.Method == http.MethodGet && strings.HasPrefix(r.URL.Path, "/plugins/"):
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/plugins":
			var body kong.Plugin
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writes = append(writes, "POST /plugins "+*body.Name)
			_, _ = w.Write([]byte(`{"id":"p-1"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/consumers/consumer-1/acls":
			var body kong.ACLGroup
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			writes = append(writes, "POST acl "+*body.Group)
			_, _ = w.Write([]byte(`{"id":"acl-new","group":"` + *body.Group + `"}`))
		case r.Method == http.MethodDelete:
			writes = append(writes, "DELETE "+r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.String())
		}
	}))

	return server, &writes
}

func expectSyncReads(s *storagemocks.MockStorage, own []v1.AllowedModel, rules string, endpoints ...string) {
	expectOwnerActive(s, "key-1", true)
	expectModelRules(s, own, rules, nil)
	expectPermission(s, "user-1", "workspace-a", "model:invoke", len(endpoints) > 0)

	if len(endpoints) > 0 {
		eps := make([]v1.Endpoint, 0, len(endpoints))
		for _, name := range endpoints {
			eps = append(eps, v1.Endpoint{Metadata: &v1.Metadata{Name: name, Workspace: "workspace-a"}})
		}

		s.On("ListEndpoint", workspaceListOption("workspace-a")).Return(eps, nil).Once()
		s.On("ListExternalEndpoint", workspaceListOption("workspace-a")).Return([]v1.ExternalEndpoint{}, nil).Once()
	}
}

// A key reaches no endpoint until its model rules are in Kong: the access
// plugin is written before any ACL group is granted.
func TestSyncAPIKeyWritesModelRulesBeforeACLGroups(t *testing.T) {
	server, writes := syncOrderServer(t, ``, ``)
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	own := []v1.AllowedModel{{Model: "a"}}
	s := storagemocks.NewMockStorage(t)
	expectSyncReads(s, own, `{"allowed_models":[{"model":"a"}],"denied_models":[],"group_token_pool":false}`, "chat-a")

	err = (&Kong{kongClient: client, storage: s}).SyncAPIKey(groupPolicyKey(own))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"POST /plugins neutree-ai-access",
		"POST acl " + BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a"),
	}, *writes)
}

// When the key loses access, its ACL groups go first and its model rules are
// lifted only after: the key's own instance is rewritten with every limit off,
// and the stale one is pruned.
func TestSyncAPIKeyRevokesACLGroupsBeforeLiftingModelRules(t *testing.T) {
	group := BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a")
	server, writes := syncOrderServer(t,
		`{"id":"acl-1","group":"`+group+`"}`,
		`{"id":"old-access","instance_name":"neutree-ai-access-old","name":"neutree-ai-access"}`)
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	expectSyncReads(s, nil, `{"allowed_models":null,"denied_models":[],"group_token_pool":false}`)

	err = (&Kong{kongClient: client, storage: s}).SyncAPIKey(groupPolicyKey(nil))

	require.NoError(t, err)
	assert.Equal(t, []string{
		"DELETE /consumers/consumer-1/acls/" + group,
		"POST /plugins neutree-ai-access",
		"DELETE /plugins/old-access",
	}, *writes)
}
