package gateway

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/mock"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	storagemocks "github.com/neutree-ai/neutree/pkg/storage/mocks"
)

func groupPolicyKey(own []v1.AllowedModel) *v1.ApiKey {
	return &v1.ApiKey{
		ID:       "key-1",
		UserID:   "user-1",
		Metadata: &v1.Metadata{Workspace: "workspace-a"},
		Spec:     &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{AllowedModels: own}},
		Status:   &v1.ApiKeyStatus{SkValue: "sk-1"},
	}
}

func expectModelRules(s *storagemocks.MockStorage, own []v1.AllowedModel, body string, err error) {
	s.On("CallDatabaseFunction", "get_api_key_model_rules", map[string]interface{}{
		"p_owner_id":       "user-1",
		"p_workspace":      "workspace-a",
		"p_allowed_models": own,
	}, mock.Anything).Run(func(args mock.Arguments) {
		if err == nil {
			// As the storage client does with the RPC body.
			_ = json.Unmarshal([]byte(body), args.Get(2))
		}
	}).Return(err).Once()
}

func TestEffectiveModelRules(t *testing.T) {
	own := []v1.AllowedModel{{Model: "a", Type: "internal", EndpointName: "ep-a"}, {Model: "b"}}

	t.Run("a key without owner or workspace keeps its own list", func(t *testing.T) {
		k := &Kong{storage: storagemocks.NewMockStorage(t)}

		got, err := k.effectiveModelRules(&v1.ApiKey{ID: "k", Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{AllowedModels: own}}})
		require.NoError(t, err)
		assert.Equal(t, modelRules{Allowed: own}, got)
	})

	cases := []struct {
		name string
		own  []v1.AllowedModel
		body string
		want modelRules
	}{
		{"no rule anywhere", nil, `{"allowed_models":null,"denied_models":[],"group_token_pool":false}`, modelRules{Denied: []string{}}},
		{"group rules narrow the key", own, `{"allowed_models":[{"model":"a","type":"internal","endpoint_name":"ep-a"}],"denied_models":["b"],"group_token_pool":false}`,
			modelRules{Allowed: own[:1], Denied: []string{"b"}}},
		{"group rules allow nothing", own, `{"allowed_models":[],"denied_models":[],"group_token_pool":false}`, modelRules{Allowed: []v1.AllowedModel{}, Denied: []string{}}},
		{"group allow on a key without a list", nil, `{"allowed_models":[{"model":"c"}],"denied_models":[],"group_token_pool":false}`,
			modelRules{Allowed: []v1.AllowedModel{{Model: "c"}}, Denied: []string{}}},
		{"a group token pool", nil, `{"allowed_models":null,"denied_models":[],"group_token_pool":true}`,
			modelRules{Denied: []string{}, GroupTokenPool: true}},
		{"deny only leaves the allowlist unset", nil, `{"allowed_models":null,"denied_models":["a"],"group_token_pool":false}`, modelRules{Denied: []string{"a"}}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := storagemocks.NewMockStorage(t)
			expectModelRules(s, c.own, c.body, nil)

			got, err := (&Kong{storage: s}).effectiveModelRules(groupPolicyKey(c.own))
			require.NoError(t, err)
			assert.Equal(t, c.want, got)
		})
	}

	for _, body := range []string{`{"code":"PGRST202","message":"not found"}`, `null`, `[]`, `{"allowed_models":null}`,
		`{"allowed_models":null,"denied_models":[]}`} {
		t.Run("an unexpected body is an error: "+body, func(t *testing.T) {
			s := storagemocks.NewMockStorage(t)
			expectModelRules(s, nil, body, nil)

			_, err := (&Kong{storage: s}).effectiveModelRules(groupPolicyKey(nil))
			assert.Error(t, err)
		})
	}
}

// The guarantee: when the group rules cannot be read, nothing reaches Kong, so
// it keeps the last config instead of the key's own looser allowlist.
func TestSyncAPIKeyStopsWhenGroupPolicyFails(t *testing.T) {
	var requests int

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++

		w.WriteHeader(http.StatusInternalServerError)
	}))
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)

	s := storagemocks.NewMockStorage(t)
	expectOwnerActive(s, "key-1", true)
	expectModelRules(s, nil, "", errors.New("connection refused"))

	err = (&Kong{kongClient: client, storage: s}).SyncAPIKey(groupPolicyKey(nil))

	require.Error(t, err)
	assert.Contains(t, err.Error(), "group policy")
	assert.Zero(t, requests, "no Kong request may be sent")
}

func TestGenerateAPIKeyAccessPluginGroupRulesOnly(t *testing.T) {
	k := &Kong{}
	cid := pointy.String("consumer-1")

	// A key without limits still gets the plugin when its groups restrict it.
	p := k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{ID: "k"}, modelRules{Allowed: []v1.AllowedModel{{Model: "a"}}})
	require.NotNil(t, p)
	assert.Equal(t, []map[string]interface{}{{"model": "a"}}, p.Config["allowed_models"])
	assert.Equal(t, []string{}, p.Config["denied_models"])
	assert.Equal(t, false, p.Config["disabled"])

	// The effective list wins over the key's own.
	own := &v1.ApiKey{ID: "k", Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{AllowedModels: []v1.AllowedModel{{Model: "a"}, {Model: "b"}}}}}
	p = k.generateAPIKeyAccessPlugin(cid, own, modelRules{Allowed: []v1.AllowedModel{}})
	require.NotNil(t, p)
	assert.Equal(t, []map[string]interface{}{}, p.Config["allowed_models"])

	// Deny only: the plugin carries the deny list and no allowlist, so models
	// the workspace does not list (pass-through endpoints) still pass.
	p = k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{ID: "k"}, modelRules{Denied: []string{"a"}})
	require.NotNil(t, p)
	assert.Nil(t, p.Config["allowed_models"])
	assert.Equal(t, []string{"a"}, p.Config["denied_models"])

	// An empty deny list alone restricts nothing: the plugin has every limit off.
	p = k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{ID: "k"}, modelRules{Denied: []string{}})
	require.NotNil(t, p)
	assert.Nil(t, p.Config["allowed_models"])
	assert.Equal(t, []string{}, p.Config["denied_models"])
}

// A key without a quota of its own gets the quota plugin when a group of its
// owner has a token pool.
func TestGenerateAPIKeyQuotaPluginGroupPool(t *testing.T) {
	cid := pointy.String("consumer-1")
	k := &Kong{quotaAPIURL: "http://postgrest:6432", serviceToken: "tok"}

	assert.Nil(t, k.generateAPIKeyQuotaPlugin(cid, &v1.ApiKey{ID: "k"}, modelRules{}))

	p := k.generateAPIKeyQuotaPlugin(cid, &v1.ApiKey{ID: "k"}, modelRules{GroupTokenPool: true})
	require.NotNil(t, p)
	assert.Equal(t, "neutree-ai-quota", *p.Name)
}

// A ServiceAccount key takes the rules of the ServiceAccount's groups.
func TestEffectiveModelRulesServiceAccountKey(t *testing.T) {
	own := []v1.AllowedModel{{Model: "a"}, {Model: "b"}}

	s := storagemocks.NewMockStorage(t)
	s.On("CallDatabaseFunction", "get_api_key_model_rules", map[string]interface{}{
		"p_owner_id":       "sa-1",
		"p_workspace":      "workspace-a",
		"p_allowed_models": own,
	}, mock.Anything).Run(func(args mock.Arguments) {
		_ = json.Unmarshal([]byte(`{"allowed_models":[{"model":"a"}],"denied_models":["b"],"group_token_pool":true}`), args.Get(2))
	}).Return(nil).Once()

	got, err := (&Kong{storage: s}).effectiveModelRules(&v1.ApiKey{
		ID:               "sa-key",
		ServiceAccountID: "sa-1",
		Metadata:         &v1.Metadata{Workspace: "workspace-a"},
		Spec:             &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{AllowedModels: own}},
	})

	require.NoError(t, err)
	assert.Equal(t, modelRules{Allowed: own[:1], Denied: []string{"b"}, GroupTokenPool: true}, got)
}
