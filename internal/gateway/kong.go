package gateway

import (
	"context"
	"fmt"
	"sort"
	"strconv"
	"strings"

	"github.com/kong/go-kong/kong"
	"github.com/pkg/errors"
	"go.openly.dev/pointy"
	"k8s.io/klog/v2"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
	"github.com/neutree-ai/neutree/pkg/storage"
)

var _ Gateway = &Kong{}

func init() { //nolint:gochecknoinits
	registerGateway("kong", newKong)
}

type Kong struct {
	kongClient        *kong.Client
	storage           storage.Storage
	logRemoteWriteUrl string

	proxyUrl string

	quotaAPIURL  string
	serviceToken string
}

func newKong(opts GatewayOptions) (Gateway, error) {
	kongClient, err := kong.NewClient(&opts.AdminUrl, nil)
	if err != nil {
		return nil, err
	}

	return &Kong{
		kongClient:        kongClient,
		storage:           opts.Storage,
		logRemoteWriteUrl: opts.LogRemoteWriteUrl,
		proxyUrl:          opts.ProxyUrl,
		quotaAPIURL:       opts.QuotaAPIURL,
		serviceToken:      opts.ServiceToken,
	}, nil
}

func (k *Kong) Init() error {
	var plugins []*kong.Plugin
	plugins = append(plugins, k.generateKeyAuthenticationPlugin())
	plugins = append(plugins, k.generateRewriteApiKeyHeaderPlugin())
	plugins = append(plugins, k.generateHttpLogPlugin())

	for _, plugin := range plugins {
		err := k.syncPlugin(plugin)
		if err != nil {
			return errors.Wrapf(err, "failed to sync plugin %s", *plugin.Name)
		}
	}

	return nil
}

func (k *Kong) SyncAPIKey(apiKey *v1.ApiKey) error {
	if apiKey == nil || apiKey.ID == "" {
		return errors.New("api key id is required")
	}

	if apiKey.Status == nil || apiKey.Status.SkValue == "" {
		return errors.New("api key status.sk_value is required")
	}

	consumer, err := k.kongClient.Consumers.GetByCustomID(context.Background(), &apiKey.ID)
	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get consumer by custom id %s", apiKey.ID)
	}

	if isResourceNotFoundError(err) {
		consumer = &kong.Consumer{
			CustomID: &apiKey.ID,
		}

		consumer, err = k.kongClient.Consumers.Create(context.Background(), consumer)
		if err != nil {
			return errors.Wrapf(err, "failed to create consumer by custom id %s", apiKey.ID)
		}
	}

	keyAuthList, _, err := k.kongClient.KeyAuths.ListForConsumer(context.Background(), consumer.ID, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to list key auths for consumer %s", *consumer.CustomID)
	}

	for _, keyAuth := range keyAuthList {
		if keyAuth.Key != nil && apiKey.Status != nil && *keyAuth.Key == apiKey.Status.SkValue {
			return k.syncAPIKeyGatewayConfig(consumer.ID, apiKey)
		}
	}

	keyAuth := &kong.KeyAuth{
		Key: &apiKey.Status.SkValue,
	}

	_, err = k.kongClient.KeyAuths.Create(context.Background(), consumer.ID, keyAuth)
	if err != nil {
		return errors.Wrapf(err, "failed to create key auth for consumer %s", *consumer.CustomID)
	}

	return k.syncAPIKeyGatewayConfig(consumer.ID, apiKey)
}

func (k *Kong) syncAPIKeyACLGroups(consumerID *string, apiKey *v1.ApiKey) error {
	desiredGroups, err := k.desiredAPIKeyACLGroups(apiKey)
	if err != nil {
		return err
	}

	currentGroups, _, err := k.kongClient.ACLs.ListForConsumer(context.Background(), consumerID, nil)
	if err != nil {
		return errors.Wrapf(err, "failed to list ACL groups for api key %s", apiKey.ID)
	}

	toCreate, toDelete := diffNeutreeACLGroups(currentGroups, desiredGroups)
	for _, group := range toCreate {
		_, err = k.kongClient.ACLs.Create(context.Background(), consumerID, &kong.ACLGroup{
			Group: pointy.String(group),
		})
		if err != nil {
			return errors.Wrapf(err, "failed to add ACL group %s to api key %s", group, apiKey.ID)
		}
	}

	for _, group := range toDelete {
		if group.Group == nil {
			continue
		}

		err = k.kongClient.ACLs.Delete(context.Background(), consumerID, group.Group)
		if err != nil {
			return errors.Wrapf(err, "failed to delete ACL group %s from api key %s", *group.Group, apiKey.ID)
		}
	}

	return nil
}

// syncAPIKeyGatewayConfig reconciles everything we push to the gateway for an
// API key: its ACL groups (authorization) and its limit plugins (quota/access).
func (k *Kong) syncAPIKeyGatewayConfig(consumerID *string, apiKey *v1.ApiKey) error {
	if err := k.syncAPIKeyACLGroups(consumerID, apiKey); err != nil {
		return err
	}

	return k.syncAPIKeyLimitPlugins(consumerID, apiKey)
}

// isManagedAPIKeyLimitPlugin reports whether a consumer plugin is one this
// controller manages from api_key.spec.limits (so stale ones can be pruned).
func isManagedAPIKeyLimitPlugin(p *kong.Plugin) bool {
	if p == nil || p.InstanceName == nil {
		return false
	}

	return strings.HasPrefix(*p.InstanceName, "neutree-ai-access-") ||
		strings.HasPrefix(*p.InstanceName, "neutree-ai-quota-")
}

// generateAPIKeyAccessPlugin builds the per-consumer neutree-ai-access plugin
// from the key's static access limits (disabled / allowed models / concurrency /
// RPS+RPM). Returns nil when the key has no access limits (so the plugin is
// absent and the key is unrestricted on the access dimension).
func (k *Kong) generateAPIKeyAccessPlugin(consumerID *string, apiKey *v1.ApiKey) *kong.Plugin {
	if apiKey.Spec == nil || apiKey.Spec.Limits == nil {
		return nil
	}

	l := apiKey.Spec.Limits
	// Emit a COMPLETE config with explicit empty values for "off" (not null) so an
	// update always overwrites prior config. syncPlugin reconciles via JSON
	// merge-patch (RFC 7386), under which an omitted key keeps the stale value;
	// using 0 / false avoids depending on null-clearing to turn a limit back off,
	// and the handler treats zero as "unrestricted".
	//
	// allowed_models is the exception: [] now means deny-all (not "off"), so it
	// can't double as the cleared value. Absent (nil) => unrestricted, sent as
	// JSON null (the handler treats null/non-array as unrestricted, and merge-patch
	// null clears any stale list); a non-nil slice — including an explicit empty
	// [] (deny-all) — is an active restriction the gateway must enforce.
	cfg := map[string]interface{}{
		"disabled":       l.Disabled,
		"allowed_models": nil,
		"concurrency":    0,
		"rate_limits":    []map[string]interface{}{},
	}
	needed := l.Disabled

	if l.AllowedModels != nil {
		cfg["allowed_models"] = l.AllowedModels
		needed = true
	}

	if l.Concurrency > 0 {
		cfg["concurrency"] = l.Concurrency
		needed = true
	}

	rateLimits := make([]map[string]interface{}, 0, 2)
	if l.RPS > 0 {
		rateLimits = append(rateLimits, map[string]interface{}{"limit": l.RPS, "window": "second"})
	}

	if l.RPM > 0 {
		rateLimits = append(rateLimits, map[string]interface{}{"limit": l.RPM, "window": "minute"})
	}

	if len(rateLimits) > 0 {
		cfg["rate_limits"] = rateLimits
		needed = true
	}

	// No active limit -> no plugin (syncAPIKeyLimitPlugins deletes any stale one),
	// so an unconfigured key stays unrestricted (by design).
	if !needed {
		return nil
	}

	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-access"),
		InstanceName: pointy.String("neutree-ai-access-" + util.HashString(apiKey.ID)),
		Consumer:     &kong.Consumer{ID: consumerID},
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config:       cfg,
	}
}

// generateAPIKeyQuotaPlugin builds the per-consumer neutree-ai-quota plugin when
// the key has a token quota. The plugin pulls the dynamic remaining count from
// neutree-api at request time. Returns nil when there is no token quota, or
// when the neutree-api URL / service token are not configured (degrade to "no
// quota enforcement" rather than mis-enforce).
func (k *Kong) generateAPIKeyQuotaPlugin(consumerID *string, apiKey *v1.ApiKey) *kong.Plugin {
	if apiKey.Spec == nil || apiKey.Spec.Limits == nil || apiKey.Spec.Limits.TokenQuota == nil {
		return nil
	}

	if apiKey.Spec.Limits.TokenQuota.Limit <= 0 {
		return nil
	}

	if k.quotaAPIURL == "" || k.serviceToken == "" {
		return nil
	}

	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-quota"),
		InstanceName: pointy.String("neutree-ai-quota-" + util.HashString(apiKey.ID)),
		Consumer:     &kong.Consumer{ID: consumerID},
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"api_url":       strings.TrimRight(k.quotaAPIURL, "/"),
			"service_token": k.serviceToken,
			"cache_ttl":     5,
		},
	}
}

// syncAPIKeyLimitPlugins reconciles the key's per-consumer limit plugins: upsert
// the desired ones and prune managed plugins that are no longer desired.
func (k *Kong) syncAPIKeyLimitPlugins(consumerID *string, apiKey *v1.ApiKey) error {
	desired := make(map[string]*kong.Plugin)
	if p := k.generateAPIKeyAccessPlugin(consumerID, apiKey); p != nil {
		desired[*p.InstanceName] = p
	}

	if p := k.generateAPIKeyQuotaPlugin(consumerID, apiKey); p != nil {
		desired[*p.InstanceName] = p
	}

	for _, p := range desired {
		if err := k.syncPlugin(p); err != nil {
			return err
		}
	}

	curPlugins, err := k.kongClient.Plugins.ListAllForConsumer(context.Background(), consumerID)
	if err != nil {
		return errors.Wrapf(err, "failed to list plugins for api key %s", apiKey.ID)
	}

	for _, cur := range curPlugins {
		if !isManagedAPIKeyLimitPlugin(cur) {
			continue
		}

		if _, ok := desired[*cur.InstanceName]; !ok {
			if err = k.kongClient.Plugins.Delete(context.Background(), cur.ID); err != nil {
				return errors.Wrapf(err, "failed to delete plugin %s for api key %s", *cur.InstanceName, apiKey.ID)
			}
		}
	}

	return nil
}

func (k *Kong) DeleteAPIKey(apiKey *v1.ApiKey) error {
	consumer, err := k.kongClient.Consumers.GetByCustomID(context.Background(), &apiKey.ID)
	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get consumer by custom id %s", apiKey.ID)
	}

	if isResourceNotFoundError(err) {
		return nil
	}

	err = k.kongClient.Consumers.Delete(context.Background(), consumer.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to delete consumer by custom id %s", apiKey.ID)
	}

	return nil
}

func (k *Kong) SyncCluster(cluster *v1.Cluster) error {
	// not implemented
	return nil
}

func (k *Kong) DeleteCluster(cluster *v1.Cluster) error {
	// not implemented
	return nil
}

func (k *Kong) SyncEndpoint(ep *v1.Endpoint) error {
	gwService, err := k.syncEndpointService(ep)
	if err != nil {
		return errors.Wrapf(err, "failed to get gateway service by endpoint %s", ep.Metadata.Name)
	}

	route, err := k.syncEndpointRoute(ep, gwService)
	if err != nil {
		return errors.Wrapf(err, "failed to sync endpoint route %s", ep.Metadata.Name)
	}

	// sync route plugins
	needPluginMap := make(map[string]*kong.Plugin)

	aiGatewayPlugin := k.generateAIGatewayPlugin(ep, route)
	needPluginMap[*aiGatewayPlugin.InstanceName] = aiGatewayPlugin

	aclPlugin := k.generateEndpointACLPlugin(ep, route)
	needPluginMap[*aclPlugin.InstanceName] = aclPlugin

	for _, plugin := range needPluginMap {
		err = k.syncPlugin(plugin)
		if err != nil {
			return errors.Wrapf(err, "failed to sync plugin %s", *plugin.Name)
		}
	}

	curPlugins, err := k.kongClient.Plugins.ListAllForRoute(context.Background(), route.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to list plugins for route %s", *route.Name)
	}

	var needDeletePlugins []*kong.Plugin

	for _, curPlugin := range curPlugins {
		if !isManagedAIRoutePlugin(curPlugin) {
			continue
		}

		if _, ok := needPluginMap[*curPlugin.InstanceName]; !ok {
			needDeletePlugins = append(needDeletePlugins, curPlugin)
		}
	}

	for _, needDeletePlugin := range needDeletePlugins {
		err = k.kongClient.Plugins.Delete(context.Background(), needDeletePlugin.ID)
		if err != nil {
			return errors.Wrapf(err, "failed to delete plugin %s", *needDeletePlugin.Name)
		}
	}

	return nil
}

func (k *Kong) GetEndpointServeUrl(ep *v1.Endpoint) (string, error) {
	return k.proxyUrl + getEndpointRoutePath(ep), nil
}

func (k *Kong) DeleteEndpoint(ep *v1.Endpoint) error {
	err := k.deleteEndpointRoute(ep)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint route %s", ep.Metadata.Name)
	}

	err = k.deleteEndpointService(ep)
	if err != nil {
		return errors.Wrapf(err, "failed to delete endpoint service %s", ep.Metadata.Name)
	}

	return nil
}

func (k *Kong) generateKeyAuthenticationPlugin() *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("key-auth"),
		InstanceName: pointy.String("neutree-key-auth"),
		Config: map[string]interface{}{
			"key_names":        []string{"kong_apikey"},
			"key_in_header":    pointy.Bool(true),
			"hide_credentials": pointy.Bool(true),
			"key_in_query":     pointy.Bool(true),
			"run_on_preflight": pointy.Bool(true),
		},
	}
}

func (k *Kong) generateRewriteApiKeyHeaderPlugin() *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("pre-function"),
		InstanceName: pointy.String("neutree-rewrite-api-key-header"),
		Config: map[string]interface{}{
			"access": []string{
				`local auth_header = kong.request.get_header("Authorization")
if auth_header then
  local _, _, token = string.find(auth_header, "Bearer%s+(.+)")
  if token then
    kong.service.request.set_header("kong_apikey", token)
    kong.service.request.clear_header("Authorization")
  end
end
local x_api_key = kong.request.get_header("x-api-key")
if x_api_key then
  kong.service.request.set_header("kong_apikey", x_api_key)
  kong.service.request.clear_header("x-api-key")
end`,
			},
		},
	}
}

// Endpoint-type tokens for the IE/EE dimension of the API key model allowlist.
// The gateway plugin stamps one of these into every endpoint route's config and
// stashes it for neutree-ai-access to match against AllowedModel.Type.
//
// This is a distinct vocabulary from the ACL resource strings ("endpoint" /
// "external-endpoint") and from the DB `source` labels that get_workspace_models
// (migration 070) returns to populate the UI dropdown ("endpoint" /
// "external_endpoint"). The UI maps that source to this type when it writes an
// AllowedModel: source "endpoint" -> internal, "external_endpoint" -> external.
const (
	endpointTypeInternal = "internal"
	endpointTypeExternal = "external"
)

func (k *Kong) generateAIGatewayPlugin(ep *v1.Endpoint, curRoute *kong.Route) *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-gateway"),
		InstanceName: pointy.String("neutree-ai-gateway-" + util.HashString(ep.Key())),
		Route:        curRoute,
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"route_type": getEndpointRouteType(ep),
			// route_prefix lets the plugin strip the endpoint route path from the
			// request URI so it can detect the Anthropic-compatible suffix
			// (/anthropic/v1/messages) on internal endpoints, matching the external
			// endpoint behavior. Without it the suffix never matches and the raw
			// Anthropic request is forwarded to the engine, which 404s.
			"route_prefix": getEndpointRoutePath(ep),
			// endpoint_type/endpoint_name identify the IE/EE this route serves so
			// the consumer-scoped neutree-ai-access plugin can enforce endpoint-level
			// model allowlists. The gateway plugin stashes these into kong.ctx.shared
			// for the access plugin (which is not bound to a route) to read.
			"endpoint_type": endpointTypeInternal,
			"endpoint_name": ep.Metadata.Name,
		},
	}
}

func (k *Kong) generateEndpointACLPlugin(ep *v1.Endpoint, curRoute *kong.Route) *kong.Plugin {
	group := BuildNeutreeACLGroup(ep.Metadata.Workspace, ACLResourceEndpoint, ep.Metadata.Name)
	return k.generateACLPlugin("neutree-acl-"+util.HashString(ep.Key()), curRoute, group)
}

func (k *Kong) generateExternalEndpointACLPlugin(ee *v1.ExternalEndpoint, curRoute *kong.Route) *kong.Plugin {
	group := BuildNeutreeACLGroup(ee.Metadata.Workspace, ACLResourceExternalEndpoint, ee.Metadata.Name)
	return k.generateACLPlugin("neutree-acl-"+util.HashString(ee.Key()), curRoute, group)
}

func (k *Kong) generateACLPlugin(instanceName string, curRoute *kong.Route, group string) *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("acl"),
		InstanceName: pointy.String(instanceName),
		Route:        curRoute,
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"allow":              []string{group},
			"hide_groups_header": true,
		},
	}
}

func (k *Kong) generateHttpLogPlugin() *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("http-log"),
		InstanceName: pointy.String("neutree-http-log"),
		Config: map[string]interface{}{
			"method":        "POST",
			"http_endpoint": k.logRemoteWriteUrl,
			"content_type":  "application/json",
			"timeout":       10000,
			"keepalive":     60000,
			"queue": map[string]interface{}{
				"initial_retry_delay":  0.1,
				"max_entries":          10000,
				"max_coalescing_delay": 1,
				"max_batch_size":       1,
				"max_retry_time":       60,
				"concurrency_limit":    -1,
				"max_retry_delay":      60,
			},
		},
	}
}

func (k *Kong) syncPlugin(plugin *kong.Plugin) error {
	curPlugin, err := k.kongClient.Plugins.Get(context.Background(), plugin.InstanceName)
	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get plugin by name %s", *plugin.InstanceName)
	}

	if isResourceNotFoundError(err) {
		_, err = k.kongClient.Plugins.Create(context.Background(), plugin)
		if err != nil {
			return errors.Wrapf(err, "failed to create plugin by name %s", *plugin.InstanceName)
		}

		return nil
	}

	// Merge desired config into current to preserve Kong's internal fields,
	// then normalize both sides to handle Kong's storage quirks
	// (explicit nulls for unset fields, nil maps stored as empty objects {}).
	err = util.JsonMerge(curPlugin.Config, plugin.Config, &plugin.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to merge plugin config")
	}

	normalizedCur, err := util.NormalizeJSON(curPlugin.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to normalize current plugin config")
	}

	normalizedMerged, err := util.NormalizeJSON(plugin.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to normalize merged plugin config")
	}

	result, diff, err := util.JsonEqual(normalizedCur, normalizedMerged)
	if err != nil {
		return errors.Wrapf(err, "failed to compare plugin config")
	}

	if !result {
		klog.Infof("plugin config changed, updating plugin: %s, diff: %s", *plugin.InstanceName, diff)

		curPlugin.Config = plugin.Config

		_, err = k.kongClient.Plugins.Update(context.Background(), curPlugin)
		if err != nil {
			return errors.Wrapf(err, "failed to update plugin by name %s", *plugin.InstanceName)
		}
	}

	return nil
}

func isManagedAIRoutePlugin(plugin *kong.Plugin) bool {
	if plugin == nil || plugin.Name == nil {
		return false
	}

	switch *plugin.Name {
	case "neutree-ai-gateway", "neutree-ai-statistics":
		return plugin.InstanceName != nil
	case "acl":
		return plugin.InstanceName != nil && strings.HasPrefix(*plugin.InstanceName, "neutree-acl-")
	default:
		return false
	}
}

func (k *Kong) syncEndpointRoute(ep *v1.Endpoint, gwService *kong.Service) (*kong.Route, error) {
	route := &kong.Route{
		Name:      pointy.String("neutree-endpoint-" + util.HashString(ep.Key())),
		Paths:     []*string{pointy.String(getEndpointRoutePath(ep))},
		Service:   gwService,
		Protocols: []*string{pointy.String("http"), pointy.String("https")},
	}

	curRoute, err := k.kongClient.Routes.Get(context.Background(), route.Name)
	if err != nil && !isResourceNotFoundError(err) {
		return nil, errors.Wrapf(err, "failed to get route by name %s", *route.Name)
	}

	if isResourceNotFoundError(err) {
		curRoute, err = k.kongClient.Routes.Create(context.Background(), route)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create route by name %s", *route.Name)
		}
	}

	if *curRoute.Paths[0] != *route.Paths[0] || *curRoute.Service.ID != *route.Service.ID {
		curRoute.Paths = route.Paths
		curRoute.Service = route.Service

		_, err = k.kongClient.Routes.Update(context.Background(), curRoute)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to update route by name %s", *route.Name)
		}
	}

	return curRoute, nil
}

func (k *Kong) deleteEndpointRoute(ep *v1.Endpoint) error {
	routeName := "neutree-endpoint-" + util.HashString(ep.Key())
	route, err := k.kongClient.Routes.Get(context.Background(), pointy.String(routeName))

	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get route by name %s", routeName)
	}

	if isResourceNotFoundError(err) {
		return nil
	}

	err = k.kongClient.Routes.Delete(context.Background(), route.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to delete route by name %s", routeName)
	}

	return nil
}

func (k *Kong) syncEndpointService(ep *v1.Endpoint) (*kong.Service, error) {
	clusters, err := k.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(ep.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(ep.Metadata.Workspace),
			},
		},
	})
	if err != nil {
		return nil, errors.Wrapf(err, "failed to list cluster by name %s", ep.Spec.Cluster)
	}

	if len(clusters) == 0 {
		return nil, errors.New("cluster not found")
	}

	if clusters[0].Status == nil {
		return nil, errors.New("cluster is never initialized")
	}

	scheme, host, port, err := util.GetClusterServeAddress(&clusters[0])
	if err != nil {
		return nil, errors.Wrapf(err, "failed to get cluster serve url")
	}

	gwServiceName := "neutree-endpoint-" + util.HashString(ep.Key())
	gwService := &kong.Service{
		Name:        &gwServiceName,
		Host:        &host,
		Port:        &port,
		Protocol:    &scheme,
		Path:        pointy.String(fmt.Sprintf("/%s/%s", ep.Metadata.Workspace, ep.Metadata.Name)),
		ReadTimeout: pointy.Int(60000 * 60),
	}

	curGwService, err := k.kongClient.Services.Get(context.Background(), &gwServiceName)
	if err != nil && !isResourceNotFoundError(err) {
		return nil, errors.Wrapf(err, "failed to get service by name %s", gwServiceName)
	}

	if isResourceNotFoundError(err) {
		curGwService, err = k.kongClient.Services.Create(context.Background(), gwService)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create service by name %s", gwServiceName)
		}
	}

	if *curGwService.Host != *gwService.Host || *curGwService.Port != *gwService.Port ||
		*curGwService.Protocol != *gwService.Protocol || *curGwService.Path != *gwService.Path ||
		*curGwService.ReadTimeout != *gwService.ReadTimeout {
		curGwService.Host = gwService.Host
		curGwService.Port = gwService.Port
		curGwService.Protocol = gwService.Protocol
		curGwService.Path = gwService.Path
		curGwService.ReadTimeout = gwService.ReadTimeout

		_, err = k.kongClient.Services.Update(context.Background(), curGwService)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to update service by name %s", gwServiceName)
		}
	}

	return curGwService, nil
}

func (k *Kong) deleteEndpointService(ep *v1.Endpoint) error {
	gwName := "neutree-endpoint-" + util.HashString(ep.Key())
	gw, err := k.kongClient.Services.Get(context.Background(), &gwName)

	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get service by name %s", gwName)
	}

	if isResourceNotFoundError(err) {
		return nil
	}

	err = k.kongClient.Services.Delete(context.Background(), gw.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to delete service by name %s", gwName)
	}

	return nil
}

func isResourceNotFoundError(err error) bool {
	if err == nil {
		return false
	}

	if strings.Contains(err.Error(), "Not found") {
		return true
	}

	return false
}

func getEndpointRouteType(ep *v1.Endpoint) string {
	// An engine that ships its own model (e.g. Flex) may leave Spec.Model nil;
	// there is no task to branch on, so fall through to the default route type.
	if ep.Spec.Model == nil {
		return v1.RouteTypeChatCompletions
	}

	switch ep.Spec.Model.Task {
	case v1.TextGenerationModelTask:
		return v1.RouteTypeChatCompletions
	case v1.TextEmbeddingModelTask:
		return v1.RouteTypeEmbeddings
	case v1.TextRerankModelTask:
		return v1.RouteTypeRerank
	}

	// default return text generation route type.
	return v1.RouteTypeChatCompletions
}

func getEndpointRoutePath(ep *v1.Endpoint) string {
	return "/workspace/" + ep.Metadata.Workspace + "/endpoint/" + ep.Metadata.Name
}

// SyncExternalEndpoint synchronizes an external endpoint configuration to Kong.
//
// Upstream entries are resolved independently: an entry that fails to resolve
// (its internal endpoint was deleted, its cluster was deleted, its URL is
// malformed) is reported as Failed in the returned statuses and left out of the
// gateway configuration, while every other entry is still pushed. Only when no
// entry resolves does the whole sync fail, since Kong needs at least one
// reachable target to build the service.
func (k *Kong) SyncExternalEndpoint(ee *v1.ExternalEndpoint) ([]v1.ExternalEndpointUpstreamStatus, error) {
	// An endpoint with no upstreams at all is a spec problem, not a resolution
	// failure — keep saying so explicitly rather than reporting "nothing
	// resolved" with an empty list of reasons.
	if len(ee.Spec.Upstreams) == 0 {
		return nil, errors.Errorf("external endpoint %s has no upstreams configured", ee.Key())
	}

	resolved := k.resolveExternalEndpointUpstreams(ee)
	statuses := externalEndpointUpstreamStatuses(ee, resolved)

	ready := make([]resolvedUpstream, 0, len(resolved))

	for _, r := range resolved {
		if r.err == nil {
			ready = append(ready, r)
		}
	}

	if len(ready) == 0 {
		return statuses, errors.Errorf("external endpoint %s has no resolvable upstream: %s",
			ee.Key(), joinUpstreamErrors(statuses))
	}

	pluginReady := ready

	if len(ee.Spec.ModelRoutes) > 0 {
		var err error
		pluginReady, err = expandExternalEndpointModelRoutes(ee, ready)

		if err != nil {
			return statuses, err
		}

		if len(pluginReady) == 0 {
			return statuses, errors.Errorf("external endpoint %s has no resolvable model route target", ee.Key())
		}
	}

	gwService, err := k.syncExternalEndpointService(ee, ready[0])
	if err != nil {
		return statuses, errors.Wrapf(err, "failed to sync external endpoint service %s", ee.Metadata.Name)
	}

	route, err := k.syncExternalEndpointRoute(ee, gwService)
	if err != nil {
		return statuses, errors.Wrapf(err, "failed to sync external endpoint route %s", ee.Metadata.Name)
	}

	// sync route plugins
	needPluginMap := make(map[string]*kong.Plugin)

	aiGatewayPlugin := k.generateExternalEndpointAIGatewayPlugin(ee, route, pluginReady)
	needPluginMap[*aiGatewayPlugin.InstanceName] = aiGatewayPlugin

	aclPlugin := k.generateExternalEndpointACLPlugin(ee, route)
	needPluginMap[*aclPlugin.InstanceName] = aclPlugin

	for _, plugin := range needPluginMap {
		err = k.syncPlugin(plugin)
		if err != nil {
			return statuses, errors.Wrapf(err, "failed to sync plugin %s", *plugin.Name)
		}
	}

	curPlugins, err := k.kongClient.Plugins.ListAllForRoute(context.Background(), route.ID)
	if err != nil {
		return statuses, errors.Wrapf(err, "failed to list plugins for route %s", *route.Name)
	}

	var needDeletePlugins []*kong.Plugin

	for _, curPlugin := range curPlugins {
		if !isManagedAIRoutePlugin(curPlugin) {
			continue
		}

		if _, ok := needPluginMap[*curPlugin.InstanceName]; !ok {
			needDeletePlugins = append(needDeletePlugins, curPlugin)
		}
	}

	for _, needDeletePlugin := range needDeletePlugins {
		err = k.kongClient.Plugins.Delete(context.Background(), needDeletePlugin.ID)
		if err != nil {
			return statuses, errors.Wrapf(err, "failed to delete plugin %s", *needDeletePlugin.Name)
		}
	}

	return statuses, nil
}

// expandExternalEndpointModelRoutes compiles the model-scoped control-plane
// format into the plugin's existing flat target list. Each route target gets
// a one-key model_mapping, so priority and weight are evaluated per virtual
// model target even when one provider is reused by several routes.
func expandExternalEndpointModelRoutes(ee *v1.ExternalEndpoint, ready []resolvedUpstream) ([]resolvedUpstream, error) {
	providers := make(map[string]resolvedUpstream, len(ready))

	for _, r := range ready {
		if r.entry.Name != "" {
			providers[r.entry.Name] = r
		}
	}

	configuredProviders := make(map[string]struct{}, len(ee.Spec.Upstreams))

	for _, entry := range ee.Spec.Upstreams {
		configuredProviders[entry.Name] = struct{}{}
	}

	expanded := make([]resolvedUpstream, 0)

	for _, route := range ee.Spec.ModelRoutes {
		if route.Model == "" {
			return nil, errors.Errorf("external endpoint %s has a model route with an empty model", ee.Key())
		}

		for _, target := range route.Targets {
			provider, ok := providers[target.Upstream]
			if !ok {
				if _, configured := configuredProviders[target.Upstream]; configured {
					continue
				}

				return nil, errors.Errorf("external endpoint %s model route %q references unknown upstream %q", ee.Key(), route.Model, target.Upstream)
			}

			if target.UpstreamModel == "" {
				return nil, errors.Errorf("external endpoint %s model route %q has an empty upstream_model", ee.Key(), route.Model)
			}

			entry := provider.entry
			entry.ModelMapping = map[string]string{route.Model: target.UpstreamModel}
			expanded = append(expanded, resolvedUpstream{
				entry:               entry,
				priority:            target.Priority,
				weight:              target.Weight,
				maxInflightRequests: target.MaxInflightRequests,
				scheme:              provider.scheme,
				host:                provider.host,
				port:                provider.port,
				path:                provider.path,
				internal:            provider.internal,
			})
		}
	}

	return expanded, nil
}

// DeleteExternalEndpoint removes an external endpoint configuration from Kong
func (k *Kong) DeleteExternalEndpoint(ee *v1.ExternalEndpoint) error {
	err := k.deleteExternalEndpointRoute(ee)
	if err != nil {
		return errors.Wrapf(err, "failed to delete external endpoint route %s", ee.Metadata.Name)
	}

	err = k.deleteExternalEndpointService(ee)
	if err != nil {
		return errors.Wrapf(err, "failed to delete external endpoint service %s", ee.Metadata.Name)
	}

	return nil
}

// GetExternalEndpointServeUrl returns the external endpoint serving url
func (k *Kong) GetExternalEndpointServeUrl(ee *v1.ExternalEndpoint) (string, error) {
	return k.proxyUrl + getExternalEndpointRoutePath(ee), nil
}

// resolveEndpointRef resolves an endpoint ref (internal endpoint name) to its cluster serve address.
// Returns scheme, host, port, path for the resolved endpoint.
func (k *Kong) resolveEndpointRef(workspace, endpointName string) (scheme, host string, port int, path string, err error) {
	endpoints, err := k.storage.ListEndpoint(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(endpointName),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
		},
	})
	if err != nil {
		return "", "", 0, "", errors.Wrapf(err, "failed to list endpoint by name %s", endpointName)
	}

	if len(endpoints) == 0 {
		return "", "", 0, "", fmt.Errorf("internal endpoint %s not found in workspace %s", endpointName, workspace)
	}

	ep := &endpoints[0]

	clusters, err := k.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(ep.Spec.Cluster),
			},
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(workspace),
			},
		},
	})
	if err != nil {
		return "", "", 0, "", errors.Wrapf(err, "failed to list cluster by name %s", ep.Spec.Cluster)
	}

	if len(clusters) == 0 {
		return "", "", 0, "", fmt.Errorf("cluster %s not found for endpoint %s", ep.Spec.Cluster, endpointName)
	}

	if clusters[0].Status == nil {
		return "", "", 0, "", fmt.Errorf("cluster %s is never initialized", ep.Spec.Cluster)
	}

	scheme, host, port, err = util.GetClusterServeAddress(&clusters[0])
	if err != nil {
		return "", "", 0, "", errors.Wrapf(err, "failed to get cluster serve address")
	}

	path = fmt.Sprintf("/%s/%s", workspace, endpointName)

	return scheme, host, port, path, nil
}

// resolvedUpstream is the outcome of resolving one spec upstream entry into a
// concrete gateway target. err is non-nil when the entry could not be resolved,
// in which case the address fields are meaningless and the entry must be left
// out of the Kong configuration.
type resolvedUpstream struct {
	entry v1.ExternalEndpointUpstreamEntry
	// Routing policy belongs to a model target, not the reusable provider
	// entry. Legacy entries leave these at their default values.
	priority            int
	weight              int
	maxInflightRequests int

	scheme string
	host   string
	port   int
	path   string
	// internal marks a target inside a neutree cluster, which the ai-gateway
	// plugin treats differently from a third-party API.
	internal bool

	err error
}

// resolveExternalEndpointUpstreams resolves every upstream entry independently.
// It never returns an error: a failure is recorded on the individual entry so
// the caller can push the entries that did resolve.
func (k *Kong) resolveExternalEndpointUpstreams(ee *v1.ExternalEndpoint) []resolvedUpstream {
	resolved := make([]resolvedUpstream, 0, len(ee.Spec.Upstreams))

	for _, entry := range ee.Spec.Upstreams {
		r := resolvedUpstream{entry: entry}

		switch {
		case entry.EndpointRef != nil:
			scheme, host, port, path, err := k.resolveEndpointRef(ee.Metadata.Workspace, *entry.EndpointRef)
			if err != nil {
				r.err = errors.Wrapf(err, "failed to resolve endpoint ref %s", *entry.EndpointRef)
				break
			}

			r.scheme, r.host, r.port, r.path = scheme, host, port, path
			r.internal = true
		case entry.Upstream != nil:
			uc, err := util.ParseURLComponents(entry.Upstream.URL)
			if err != nil {
				r.err = errors.Wrapf(err, "failed to parse upstream URL %s", entry.Upstream.URL)
				break
			}

			r.scheme, r.host, r.port, r.path = uc.Scheme, uc.Host, uc.Port, uc.Path
		default:
			r.err = errors.Errorf("upstream entry for model_mapping %v has neither endpoint_ref nor upstream configured", entry.ModelMapping)
		}

		resolved = append(resolved, r)
	}

	return resolved
}

// externalEndpointUpstreamStatuses projects resolution outcomes into the
// user-visible per-upstream status list, in spec order.
func externalEndpointUpstreamStatuses(ee *v1.ExternalEndpoint, resolved []resolvedUpstream) []v1.ExternalEndpointUpstreamStatus {
	statuses := make([]v1.ExternalEndpointUpstreamStatus, 0, len(resolved))
	modelsByUpstream := make(map[string]map[string]struct{})

	for _, route := range ee.Spec.ModelRoutes {
		for _, target := range route.Targets {
			if modelsByUpstream[target.Upstream] == nil {
				modelsByUpstream[target.Upstream] = make(map[string]struct{})
			}

			modelsByUpstream[target.Upstream][route.Model] = struct{}{}
		}
	}

	for i := range resolved {
		entry := resolved[i].entry
		models := entry.ExposedModels()

		if routeModels := modelsByUpstream[entry.Name]; len(routeModels) > 0 {
			models = make([]string, 0, len(routeModels))
			for model := range routeModels {
				models = append(models, model)
			}

			sort.Strings(models)
		}

		status := v1.ExternalEndpointUpstreamStatus{
			Kind:   entry.Kind(),
			Ref:    entry.Ref(),
			Models: models,
			Phase:  v1.ExternalEndpointUpstreamPhaseReady,
		}

		if resolved[i].err != nil {
			status.Phase = v1.ExternalEndpointUpstreamPhaseFailed
			status.ErrorMessage = resolved[i].err.Error()
		}

		statuses = append(statuses, status)
	}

	return statuses
}

// joinUpstreamErrors renders every failed entry into a single message, used when
// no upstream resolved at all. It reads the already-projected statuses so that
// "failed" has exactly one definition.
func joinUpstreamErrors(statuses []v1.ExternalEndpointUpstreamStatus) string {
	msgs := make([]string, 0, len(statuses))

	for _, s := range statuses {
		if s.Phase == v1.ExternalEndpointUpstreamPhaseFailed {
			msgs = append(msgs, s.ErrorMessage)
		}
	}

	return strings.Join(msgs, "; ")
}

func (k *Kong) syncExternalEndpointService(ee *v1.ExternalEndpoint, target resolvedUpstream) (*kong.Service, error) {
	// The Kong service only needs one reachable target: the ai-gateway plugin
	// rewrites the upstream per request from its own upstream list. Using the
	// first *resolvable* entry (rather than Upstreams[0]) is what keeps a broken
	// leading entry from blocking the whole config push.
	serviceScheme := target.scheme
	serviceHost := target.host
	servicePort := target.port
	servicePath := target.path

	timeout := 60000
	if ee.Spec.Timeout != nil && *ee.Spec.Timeout > 0 {
		timeout = *ee.Spec.Timeout
	}

	gwServiceName := "neutree-external-endpoint-" + util.HashString(ee.Key())
	gwService := &kong.Service{
		Name:        &gwServiceName,
		Host:        &serviceHost,
		Port:        &servicePort,
		Protocol:    &serviceScheme,
		Path:        &servicePath,
		ReadTimeout: &timeout,
	}

	curGwService, err := k.kongClient.Services.Get(context.Background(), &gwServiceName)
	if err != nil && !isResourceNotFoundError(err) {
		return nil, errors.Wrapf(err, "failed to get service by name %s", gwServiceName)
	}

	if isResourceNotFoundError(err) {
		curGwService, err = k.kongClient.Services.Create(context.Background(), gwService)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create service by name %s", gwServiceName)
		}
	}

	if *curGwService.Host != *gwService.Host || *curGwService.Port != *gwService.Port ||
		*curGwService.Protocol != *gwService.Protocol || *curGwService.Path != *gwService.Path ||
		*curGwService.ReadTimeout != *gwService.ReadTimeout {
		curGwService.Host = gwService.Host
		curGwService.Port = gwService.Port
		curGwService.Protocol = gwService.Protocol
		curGwService.Path = gwService.Path
		curGwService.ReadTimeout = gwService.ReadTimeout

		_, err = k.kongClient.Services.Update(context.Background(), curGwService)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to update service by name %s", gwServiceName)
		}
	}

	return curGwService, nil
}

func (k *Kong) deleteExternalEndpointService(ee *v1.ExternalEndpoint) error {
	gwName := "neutree-external-endpoint-" + util.HashString(ee.Key())
	gw, err := k.kongClient.Services.Get(context.Background(), &gwName)

	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get service by name %s", gwName)
	}

	if isResourceNotFoundError(err) {
		return nil
	}

	err = k.kongClient.Services.Delete(context.Background(), gw.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to delete service by name %s", gwName)
	}

	return nil
}

func (k *Kong) syncExternalEndpointRoute(ee *v1.ExternalEndpoint, gwService *kong.Service) (*kong.Route, error) {
	route := &kong.Route{
		Name:      pointy.String("neutree-external-endpoint-" + util.HashString(ee.Key())),
		Paths:     []*string{pointy.String(getExternalEndpointRoutePath(ee))},
		Service:   gwService,
		Protocols: []*string{pointy.String("http"), pointy.String("https")},
	}

	curRoute, err := k.kongClient.Routes.Get(context.Background(), route.Name)
	if err != nil && !isResourceNotFoundError(err) {
		return nil, errors.Wrapf(err, "failed to get route by name %s", *route.Name)
	}

	if isResourceNotFoundError(err) {
		curRoute, err = k.kongClient.Routes.Create(context.Background(), route)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to create route by name %s", *route.Name)
		}
	}

	if *curRoute.Paths[0] != *route.Paths[0] || *curRoute.Service.ID != *route.Service.ID {
		curRoute.Paths = route.Paths
		curRoute.Service = route.Service

		_, err = k.kongClient.Routes.Update(context.Background(), curRoute)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to update route by name %s", *route.Name)
		}
	}

	return curRoute, nil
}

func (k *Kong) deleteExternalEndpointRoute(ee *v1.ExternalEndpoint) error {
	routeName := "neutree-external-endpoint-" + util.HashString(ee.Key())
	route, err := k.kongClient.Routes.Get(context.Background(), pointy.String(routeName))

	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get route by name %s", routeName)
	}

	if isResourceNotFoundError(err) {
		return nil
	}

	err = k.kongClient.Routes.Delete(context.Background(), route.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to delete route by name %s", routeName)
	}

	return nil
}

// generateExternalEndpointAIGatewayPlugin builds the plugin config from the
// already-resolved upstreams. Callers pass only the entries that resolved, so a
// broken entry simply stops being routable while the rest keep serving.
func (k *Kong) generateExternalEndpointAIGatewayPlugin(ee *v1.ExternalEndpoint, curRoute *kong.Route,
	ready []resolvedUpstream) *kong.Plugin {
	instanceName := "neutree-ai-gateway-external-endpoint-" + util.HashString(ee.Key())

	upstreams := make([]map[string]interface{}, 0, len(ready))

	for _, r := range ready {
		weight := r.weight
		if weight == 0 {
			weight = 1
		}

		upstreamEntry := map[string]interface{}{
			"model_mapping":         r.entry.ModelMapping,
			"scheme":                r.scheme,
			"host":                  r.host,
			"port":                  r.port,
			"path":                  r.path,
			"auth_header":           nil,
			"priority":              r.priority,
			"weight":                weight,
			"max_inflight_requests": r.maxInflightRequests,
			// Must explicitly set "internal" to match Kong schema default (false),
			// otherwise the merge-patch array replacement drops it and causes a perpetual sync loop.
			"internal": r.internal,
		}

		if !r.internal && r.entry.Auth != nil {
			upstreamEntry["auth_header"] = r.entry.Auth.AuthHeaderValue()
		}

		upstreams = append(upstreams, upstreamEntry)
	}

	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-gateway"),
		InstanceName: &instanceName,
		Route:        curRoute,
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"route_prefix": getExternalEndpointRoutePath(ee),
			"upstreams":    upstreams,
			// See generateAIGatewayPlugin: identify the EE this route serves so the
			// access plugin can enforce endpoint-level allowlists. Note the per-upstream
			// "internal" flag is a routing detail; from the API key's perspective the
			// request entered through this external endpoint, so the dimension is fixed
			// to (external, ee.name) regardless of which upstream the model resolves to.
			"endpoint_type": endpointTypeExternal,
			"endpoint_name": ee.Metadata.Name,
		},
	}
}

func getExternalEndpointRoutePath(ee *v1.ExternalEndpoint) string {
	return "/workspace/" + ee.Metadata.Workspace + "/external-endpoint/" + ee.Metadata.Name
}
