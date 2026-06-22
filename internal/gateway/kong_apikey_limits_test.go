package gateway

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/kong/go-kong/kong"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"go.openly.dev/pointy"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

func TestIsManagedAPIKeyLimitPlugin(t *testing.T) {
	assert.False(t, isManagedAPIKeyLimitPlugin(nil))
	// managed plugins are identified by instance-name prefix, not name
	assert.False(t, isManagedAPIKeyLimitPlugin(&kong.Plugin{Name: pointy.String("neutree-ai-access")}))
	assert.False(t, isManagedAPIKeyLimitPlugin(&kong.Plugin{InstanceName: pointy.String("neutree-acl-route")}))
	assert.False(t, isManagedAPIKeyLimitPlugin(&kong.Plugin{InstanceName: pointy.String("other")}))
	assert.True(t, isManagedAPIKeyLimitPlugin(&kong.Plugin{InstanceName: pointy.String("neutree-ai-access-abc")}))
	assert.True(t, isManagedAPIKeyLimitPlugin(&kong.Plugin{InstanceName: pointy.String("neutree-ai-quota-abc")}))
}

func TestGenerateAPIKeyAccessPlugin(t *testing.T) {
	k := &Kong{}
	cid := pointy.String("consumer-1")

	// no spec / no limits -> no plugin
	assert.Nil(t, k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{ID: "k"}))
	assert.Nil(t, k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{ID: "k", Spec: &v1.ApiKeySpec{}}))

	// only a token quota (no access dimension) -> no access plugin
	assert.Nil(t, k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{
		ID:   "k",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{TokenQuota: &v1.ApiKeyTokenQuota{Limit: 100}}},
	}))

	// full access limits -> complete config
	apiKey := &v1.ApiKey{
		ID: "key-1",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{
			Disabled:      true,
			AllowedModels: []string{"gpt-4o"},
			Concurrency:   8,
			RPS:           10,
			RPM:           600,
		}},
	}
	p := k.generateAPIKeyAccessPlugin(cid, apiKey)
	require.NotNil(t, p)
	assert.Equal(t, "neutree-ai-access", *p.Name)
	assert.Equal(t, "neutree-ai-access-"+util.HashString("key-1"), *p.InstanceName)
	assert.Equal(t, cid, p.Consumer.ID)
	assert.ElementsMatch(t, []*string{pointy.String("http"), pointy.String("https")}, p.Protocols)
	assert.Equal(t, true, p.Config["disabled"])
	assert.Equal(t, []string{"gpt-4o"}, p.Config["allowed_models"])
	assert.Equal(t, 8, p.Config["concurrency"])
	rl, ok := p.Config["rate_limits"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, rl, 2)
	assert.Equal(t, "second", rl[0]["window"])
	assert.Equal(t, 10, rl[0]["limit"])
	assert.Equal(t, "minute", rl[1]["window"])
	assert.Equal(t, 600, rl[1]["limit"])

	// disabled only -> plugin present; the other fields are emitted as explicit empties
	p2 := k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{
		ID:   "k2",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{Disabled: true}},
	})
	require.NotNil(t, p2)
	assert.Equal(t, true, p2.Config["disabled"])
	// Cleared fields are emitted as explicit empties (not nil) so updates overwrite
	// prior config; the handler treats empty list / zero as unrestricted.
	assert.Equal(t, []string{}, p2.Config["allowed_models"])
	assert.Equal(t, 0, p2.Config["concurrency"])
	assert.Equal(t, []map[string]interface{}{}, p2.Config["rate_limits"])

	// RPS only -> single second-window rate limit; disabled present and false
	p3 := k.generateAPIKeyAccessPlugin(cid, &v1.ApiKey{
		ID:   "k3",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{RPS: 5}},
	})
	require.NotNil(t, p3)
	assert.Equal(t, false, p3.Config["disabled"])
	rl3, ok := p3.Config["rate_limits"].([]map[string]interface{})
	require.True(t, ok)
	require.Len(t, rl3, 1)
	assert.Equal(t, "second", rl3[0]["window"])
	assert.Equal(t, 5, rl3[0]["limit"])
}

func TestGenerateAPIKeyQuotaPlugin(t *testing.T) {
	cid := pointy.String("consumer-1")
	withAPI := &Kong{neutreeAPIUrl: "http://neutree-api:3000", serviceToken: "tok"}

	// no token quota -> no plugin
	assert.Nil(t, withAPI.generateAPIKeyQuotaPlugin(cid, &v1.ApiKey{
		ID:   "k",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{RPS: 5}},
	}))
	// non-positive limit -> no plugin
	assert.Nil(t, withAPI.generateAPIKeyQuotaPlugin(cid, &v1.ApiKey{
		ID:   "k",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{TokenQuota: &v1.ApiKeyTokenQuota{Limit: 0}}},
	}))

	apiKey := &v1.ApiKey{
		ID:   "key-1",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{TokenQuota: &v1.ApiKeyTokenQuota{Limit: 100, Period: "monthly"}}},
	}
	// missing api url / token -> no plugin (degrade rather than mis-enforce)
	assert.Nil(t, (&Kong{}).generateAPIKeyQuotaPlugin(cid, apiKey))
	assert.Nil(t, (&Kong{neutreeAPIUrl: "x"}).generateAPIKeyQuotaPlugin(cid, apiKey))
	assert.Nil(t, (&Kong{serviceToken: "y"}).generateAPIKeyQuotaPlugin(cid, apiKey))

	p := withAPI.generateAPIKeyQuotaPlugin(cid, apiKey)
	require.NotNil(t, p)
	assert.Equal(t, "neutree-ai-quota", *p.Name)
	assert.Equal(t, "neutree-ai-quota-"+util.HashString("key-1"), *p.InstanceName)
	assert.Equal(t, cid, p.Consumer.ID)
	assert.Equal(t, "http://neutree-api:3000", p.Config["api_url"])
	assert.Equal(t, "tok", p.Config["service_token"])
	assert.Equal(t, 5, p.Config["cache_ttl"])

	// A trailing slash on the configured URL is trimmed so the plugin builds
	// "<url>/rpc/..." rather than "<url>//rpc/...".
	kSlash := &Kong{neutreeAPIUrl: "http://neutree-api:3000/", serviceToken: "tok"}
	ps := kSlash.generateAPIKeyQuotaPlugin(cid, apiKey)
	require.NotNil(t, ps)
	assert.Equal(t, "http://neutree-api:3000", ps.Config["api_url"])
}

// TestSyncAPIKeyLimitPlugins verifies the reconcile/prune path: the desired
// plugin(s) are upserted, stale *managed* plugins are deleted, and unrelated
// plugins are left untouched. The Kong admin API is faked with httptest.
func TestSyncAPIKeyLimitPlugins(t *testing.T) {
	apiKey := &v1.ApiKey{
		ID:   "key-1",
		Spec: &v1.ApiKeySpec{Limits: &v1.ApiKeyLimits{TokenQuota: &v1.ApiKeyTokenQuota{Limit: 100, Period: "monthly"}}},
	}
	quotaInstance := "neutree-ai-quota-" + util.HashString("key-1")

	var created []string
	var deleted []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/plugins/"+quotaInstance:
			w.WriteHeader(http.StatusNotFound)
			_, _ = w.Write([]byte(`{"message":"Not found"}`))
		case r.Method == http.MethodPost && r.URL.Path == "/plugins":
			var body kong.Plugin
			require.NoError(t, json.NewDecoder(r.Body).Decode(&body))
			require.NotNil(t, body.InstanceName)
			created = append(created, *body.InstanceName)
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"id":"created-id","instance_name":"` + *body.InstanceName + `"}`))
		case r.Method == http.MethodGet && r.URL.Path == "/consumers/consumer-1/plugins":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"data":[` +
				`{"id":"stale-access","instance_name":"neutree-ai-access-stale","name":"neutree-ai-access"},` +
				`{"id":"keep-acl","instance_name":"neutree-acl-x","name":"acl"}` +
				`]}`))
		case r.Method == http.MethodDelete:
			deleted = append(deleted, r.URL.Path)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Fatalf("unexpected Kong request: %s %s", r.Method, r.URL.Path)
		}
	}))
	defer server.Close()

	client, err := kong.NewClient(pointy.String(server.URL), server.Client())
	require.NoError(t, err)
	k := &Kong{kongClient: client, neutreeAPIUrl: "http://neutree-api:3000", serviceToken: "tok"}

	err = k.syncAPIKeyLimitPlugins(pointy.String("consumer-1"), apiKey)
	require.NoError(t, err)

	assert.Equal(t, []string{quotaInstance}, created)           // desired quota plugin upserted
	assert.Equal(t, []string{"/plugins/stale-access"}, deleted) // stale managed access plugin pruned; acl untouched
}
