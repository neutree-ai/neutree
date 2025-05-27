package gateway

import (
	"context"
	"net/url"
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

func (k *Kong) SyncBackendService(cluster *v1.Cluster) error {
	dashboardURL, err := url.Parse(cluster.Status.DashboardURL)
	if err != nil {
		return errors.Wrapf(err, "failed to parse dashboard url %s", cluster.Status.DashboardURL)
	}

	gwName := "neutree-cluster-" + util.HashString(cluster.Key())
	gw := &kong.Service{
		Name:     &gwName,
		Protocol: &dashboardURL.Scheme,
		Host:     &strings.Split(dashboardURL.Host, ":")[0],
		Port:     pointy.Int(8000),
	}

	curGw, err := k.kongClient.Services.Get(context.Background(), &gwName)
	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get service by name %s", gwName)
	}

	if isResourceNotFoundError(err) {
		_, err = k.kongClient.Services.Create(context.Background(), gw)
		if err != nil {
			return errors.Wrapf(err, "failed to create service by name %s", gwName)
		}

		return nil
	}

	if *curGw.Host != *gw.Host || *curGw.Port != *gw.Port {
		curGw.Host = gw.Host
		curGw.Port = gw.Port

		_, err = k.kongClient.Services.Update(context.Background(), curGw)
		if err != nil {
			return errors.Wrapf(err, "failed to update service by name %s", gwName)
		}
	}

	return nil
}

func (k *Kong) DeleteBackendService(cluster *v1.Cluster) error {
	gwName := "neutree-cluster-" + util.HashString(cluster.Key())
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

func (k *Kong) SyncRoute(ep *v1.Endpoint) error {
	gw, err := k.getGwService(ep)
	if err != nil {
		return errors.Wrapf(err, "failed to get gateway service by endpoint %s", ep.Metadata.Name)
	}

	prefixPath := "/workspace/" + ep.Metadata.Workspace + "/endpoint"
	route := &kong.Route{
		Name:      pointy.String("neutree-endpoint-" + util.HashString(ep.Key())),
		Paths:     []*string{pointy.String(prefixPath + "/" + ep.Metadata.Name)},
		Service:   gw,
		Protocols: []*string{pointy.String("http"), pointy.String("https")},
	}

	curRoute, err := k.kongClient.Routes.Get(context.Background(), route.Name)
	if err != nil && !isResourceNotFoundError(err) {
		return errors.Wrapf(err, "failed to get route by name %s", *route.Name)
	}

	if isResourceNotFoundError(err) {
		curRoute, err = k.kongClient.Routes.Create(context.Background(), route)
		if err != nil {
			return errors.Wrapf(err, "failed to create route by name %s", *route.Name)
		}
	}

	if *curRoute.Paths[0] != *route.Paths[0] || *curRoute.Service.ID != *route.Service.ID {
		curRoute.Paths = route.Paths
		curRoute.Service = route.Service

		_, err = k.kongClient.Routes.Update(context.Background(), curRoute)
		if err != nil {
			return errors.Wrapf(err, "failed to update route by name %s", *route.Name)
		}
	}

	// sync route plugins
	needPluginMap := make(map[string]*kong.Plugin)

	reuqestTransfomerPlugin := k.generateRequestTransformPlugin(ep, curRoute)
	needPluginMap[*reuqestTransfomerPlugin.InstanceName] = reuqestTransfomerPlugin
	aiStatisticsPlugin := k.generateAIStatisticsPlugin(ep, curRoute)
	needPluginMap[*aiStatisticsPlugin.InstanceName] = aiStatisticsPlugin

	for _, plugin := range needPluginMap {
		err = k.syncPlugin(plugin)
		if err != nil {
			return errors.Wrapf(err, "failed to sync plugin %s", *plugin.Name)
		}
	}

	curPlugins, err := k.kongClient.Plugins.ListAllForRoute(context.Background(), curRoute.ID)
	if err != nil {
		return errors.Wrapf(err, "failed to list plugins for route %s", *curRoute.Name)
	}

	var needDeletePlugins []*kong.Plugin

	for _, curPlugin := range curPlugins {
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

func (k *Kong) DeleteRoute(ep *v1.Endpoint) error {
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
end`,
			},
		},
	}
}

func (k *Kong) generateAIStatisticsPlugin(ep *v1.Endpoint, curRoute *kong.Route) *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("neutree-ai-statistics"),
		InstanceName: pointy.String("neutree-ai-statistics-" + util.HashString(ep.Key())),
		Route:        curRoute,
		Protocols:    []*string{pointy.String("http"), pointy.String("https")},
		Config: map[string]interface{}{
			"route_type": getEndpointRouteType(ep),
		},
	}
}

func (k *Kong) generateRequestTransformPlugin(ep *v1.Endpoint, curRoute *kong.Route) *kong.Plugin {
	return &kong.Plugin{
		Name:         pointy.String("request-transformer"),
		InstanceName: pointy.String("neutree-request-transformer-" + util.HashString(ep.Key())),
		Route:        curRoute,
		Config: map[string]interface{}{
			"replace": map[string]interface{}{
				"uri": "/" + ep.Metadata.Name + getEndpointRouteType(ep),
			},
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

	err = util.JsonMerge(curPlugin.Config, plugin.Config, &plugin.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to merge plugin config")
	}

	result, diff, err := util.JsonEqual(curPlugin.Config, plugin.Config)
	if err != nil {
		return errors.Wrapf(err, "failed to compare plugin config")
	}

	if !result {
		klog.Info("plugin config diff: ", diff)

		curPlugin.Config = plugin.Config

		_, err = k.kongClient.Plugins.Update(context.Background(), curPlugin)
		if err != nil {
			return errors.Wrapf(err, "failed to update plugin by name %s", *plugin.InstanceName)
		}
	}

	return nil
}

func (k *Kong) getGwService(ep *v1.Endpoint) (*kong.Service, error) {
	clusterList, err := k.storage.ListCluster(storage.ListOption{
		Filters: []storage.Filter{
			{
				Column:   "metadata->workspace",
				Operator: "eq",
				Value:    strconv.Quote(ep.Metadata.Workspace),
			},
			{
				Column:   "metadata->name",
				Operator: "eq",
				Value:    strconv.Quote(ep.Spec.Cluster),
			},
		},
	})

	if err != nil {
		return nil, errors.Wrapf(err, "failed to list cluster by workspace %s", ep.Metadata.Workspace)
	}

	if len(clusterList) == 0 {
		return nil, storage.ErrResourceNotFound
	}

	cluster := clusterList[0]
	gwName := "neutree-cluster-" + util.HashString(cluster.Key())
	gw, err := k.kongClient.Services.Get(context.Background(), &gwName)

	if err != nil {
		return nil, errors.Wrapf(err, "failed to get service by name %s", gwName)
	}

	return gw, nil
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
		return "/v1/chat/completions"
	case v1.TextEmbeddingModelTask:
		return "/v1/embeddings"
	case v1.TextRerankModelTask:
		return "/v1/rerank"
	}

	// default return text generation route type.
	return "/v1/chat/completions"
}
