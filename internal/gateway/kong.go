package gateway

import (
	"context"
	"fmt"
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
			return nil
		}
	}

	keyAuth := &kong.KeyAuth{
		Key: &apiKey.Status.SkValue,
	}

	_, err = k.kongClient.KeyAuths.Create(context.Background(), consumer.ID, keyAuth)
	if err != nil {
		return errors.Wrapf(err, "failed to create key auth for consumer %s", *consumer.CustomID)
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
  end
end
local x_api_key = kong.request.get_header("x-api-key")
if x_api_key then
  kong.service.request.set_header("kong_apikey", x_api_key)
end`,
			},
		},
	}
}

func (k *Kong) generateAIGatewayPlugin(ep *v1.Endpoint, curRoute *kong.Route) *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-gateway"),
		InstanceName: pointy.String("neutree-ai-gateway-" + util.HashString(ep.Key())),
		Route:        curRoute,
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"route_type": getEndpointRouteType(ep),
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
		return true
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

// SyncExternalEndpoint synchronizes an external endpoint configuration to Kong
func (k *Kong) SyncExternalEndpoint(ee *v1.ExternalEndpoint) error {
	gwService, err := k.syncExternalEndpointService(ee)
	if err != nil {
		return errors.Wrapf(err, "failed to sync external endpoint service %s", ee.Metadata.Name)
	}

	route, err := k.syncExternalEndpointRoute(ee, gwService)
	if err != nil {
		return errors.Wrapf(err, "failed to sync external endpoint route %s", ee.Metadata.Name)
	}

	// sync route plugins
	needPluginMap := make(map[string]*kong.Plugin)

	aiGatewayPlugin, err := k.generateExternalEndpointAIGatewayPlugin(ee, route)
	if err != nil {
		return errors.Wrapf(err, "failed to generate ai gateway plugin for %s", ee.Metadata.Name)
	}

	needPluginMap[*aiGatewayPlugin.InstanceName] = aiGatewayPlugin

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

func (k *Kong) syncExternalEndpointService(ee *v1.ExternalEndpoint) (*kong.Service, error) {
	var serviceHost string
	var servicePort int
	var serviceScheme string
	var servicePath string

	if len(ee.Spec.Upstreams) == 0 {
		return nil, errors.Errorf("external endpoint %s has no upstreams configured", ee.Key())
	}

	firstEntry := ee.Spec.Upstreams[0]

	switch {
	case firstEntry.EndpointRef != nil:
		// Resolve internal endpoint ref
		scheme, host, port, path, err := k.resolveEndpointRef(ee.Metadata.Workspace, *firstEntry.EndpointRef)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to resolve endpoint ref: %s", *firstEntry.EndpointRef)
		}

		serviceScheme = scheme
		serviceHost = host
		servicePort = port
		servicePath = path
	case firstEntry.Upstream != nil:
		// Parse external upstream URL
		uc, err := util.ParseURLComponents(firstEntry.Upstream.URL)
		if err != nil {
			return nil, errors.Wrapf(err, "failed to parse upstream URL: %s", firstEntry.Upstream.URL)
		}

		serviceScheme = uc.Scheme
		serviceHost = uc.Host
		servicePort = uc.Port
		servicePath = uc.Path
	default:
		return nil, errors.Errorf("first upstream entry of external endpoint %s has neither endpoint_ref nor upstream configured", ee.Key())
	}

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

func (k *Kong) generateExternalEndpointAIGatewayPlugin(ee *v1.ExternalEndpoint, curRoute *kong.Route) (*kong.Plugin, error) {
	instanceName := "neutree-ai-gateway-external-endpoint-" + util.HashString(ee.Key())

	var upstreams []map[string]interface{}

	for _, entry := range ee.Spec.Upstreams {
		var upstreamEntry map[string]interface{}

		switch {
		case entry.EndpointRef != nil:
			// Resolve internal endpoint ref
			scheme, host, port, path, err := k.resolveEndpointRef(ee.Metadata.Workspace, *entry.EndpointRef)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to resolve endpoint ref %s for model_mapping %v", *entry.EndpointRef, entry.ModelMapping)
			}

			upstreamEntry = map[string]interface{}{
				"model_mapping": entry.ModelMapping,
				"scheme":        scheme,
				"host":          host,
				"port":          port,
				"path":          path,
				"auth_header":   nil,
				"internal":      true,
			}
		case entry.Upstream != nil:
			uc, err := util.ParseURLComponents(entry.Upstream.URL)
			if err != nil {
				return nil, errors.Wrapf(err, "failed to parse upstream URL for model_mapping %v", entry.ModelMapping)
			}

			upstreamEntry = map[string]interface{}{
				"model_mapping": entry.ModelMapping,
				"scheme":        uc.Scheme,
				"host":          uc.Host,
				"port":          uc.Port,
				"path":          uc.Path,
				"auth_header": nil,
				// Must explicitly set "internal" to match Kong schema default (false),
				// otherwise the merge-patch array replacement drops it and causes a perpetual sync loop.
				"internal": false,
			}

			if entry.Auth != nil {
				upstreamEntry["auth_header"] = entry.Auth.AuthHeaderValue()
			}
		default:
			return nil, errors.Errorf("upstream entry for model_mapping %v has neither endpoint_ref nor upstream configured", entry.ModelMapping)
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
		},
	}, nil
}

func getExternalEndpointRoutePath(ee *v1.ExternalEndpoint) string {
	return "/workspace/" + ee.Metadata.Workspace + "/external-endpoint/" + ee.Metadata.Name
}
