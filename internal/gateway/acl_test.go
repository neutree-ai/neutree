package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
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
	apiKey := &v1.ApiKey{
		ID:     "key-1",
		UserID: "user-1",
		Metadata: &v1.Metadata{
			Workspace: "workspace-a",
		},
	}

	s := storagemocks.NewMockStorage(t)
	k := &Kong{storage: s}

	expectPermission(s, "user-1", "workspace-a", "endpoint:read", true)
	expectPermission(s, "user-1", "workspace-a", "external_endpoint:read", false)
	s.On("ListEndpoint", workspaceListOption("workspace-a")).Return([]v1.Endpoint{
		{Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"}},
		{Metadata: &v1.Metadata{Name: "deleted", Workspace: "workspace-a", DeletionTimestamp: "2026-01-01T00:00:00Z"}},
	}, nil).Once()

	groups, err := k.desiredAPIKeyACLGroups(apiKey)

	assert.NoError(t, err)
	assert.Equal(t, []string{
		BuildNeutreeACLGroup("workspace-a", ACLResourceEndpoint, "chat-a"),
	}, groups)
}

func TestDesiredAPIKeyACLGroupsIncludesExternalEndpoints(t *testing.T) {
	apiKey := &v1.ApiKey{
		ID:     "key-2",
		UserID: "user-2",
		Metadata: &v1.Metadata{
			Workspace: "workspace-a",
		},
	}

	s := storagemocks.NewMockStorage(t)
	k := &Kong{storage: s}

	expectPermission(s, "user-2", "workspace-a", "endpoint:read", false)
	expectPermission(s, "user-2", "workspace-a", "external_endpoint:read", true)
	s.On("ListExternalEndpoint", workspaceListOption("workspace-a")).Return([]v1.ExternalEndpoint{
		{Metadata: &v1.Metadata{Name: "ee-a", Workspace: "workspace-a"}},
	}, nil).Once()

	groups, err := k.desiredAPIKeyACLGroups(apiKey)

	assert.NoError(t, err)
	assert.Equal(t, []string{
		BuildNeutreeACLGroup("workspace-a", ACLResourceExternalEndpoint, "ee-a"),
	}, groups)
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
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	expectPermission(s, "user-1", "workspace-a", "endpoint:read", true)
	expectPermission(s, "user-1", "workspace-a", "external_endpoint:read", false)
	s.On("ListEndpoint", workspaceListOption("workspace-a")).Return([]v1.Endpoint{
		{Metadata: &v1.Metadata{Name: "chat-a", Workspace: "workspace-a"}},
	}, nil).Once()

	k := &Kong{kongClient: client, storage: s}

	err = k.syncAPIKeyACLGroups(pointy.String("consumer-1"), apiKey)

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
