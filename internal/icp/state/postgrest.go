package state

import (
	"context"
	"fmt"
	"strings"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/utils"
	"github.com/neutree-ai/neutree/pkg/storage"
)

type PostgRESTSyncer struct {
	Storage storage.Storage
	Engine  *Engine
}

func (s *PostgRESTSyncer) buildSyncEvent() (Event, error) {
	eps, err := s.Storage.ListEndpoint(storage.ListOption{})
	if err != nil {
		return Event{}, err
	}

	clusters, err := s.Storage.ListCluster(storage.ListOption{})
	if err != nil {
		return Event{}, err
	}

	ees, err := s.Storage.ListExternalEndpoint(storage.ListOption{})
	if err != nil {
		return Event{}, err
	}

	keys, err := s.Storage.ListApiKey(storage.ListOption{})
	if err != nil {
		return Event{}, err
	}

	var groups []v1.GroupPolicy
	if err = s.Storage.GenericQuery(storage.GROUP_POLICY_TABLE, "*", nil, &groups); err != nil {
		return Event{}, err
	}

	routes := buildRoutes(eps, clusters, ees)
	apiKeys, policyGroups := s.buildKeysAndPolicies(keys, groups)

	return Event{
		Kind: EventConfig,
		Config: &ConfigEvent{
			Replace:      true,
			APIKeys:      apiKeys,
			PolicyGroups: policyGroups,
			Routes:       routes,
		},
	}, nil
}

func (s *PostgRESTSyncer) Sync() error {
	ev, err := s.buildSyncEvent()
	if err != nil {
		return err
	}

	return s.Engine.Publish(ev)
}

func (s *PostgRESTSyncer) SyncAndWait(ctx context.Context) error {
	ev, err := s.buildSyncEvent()
	if err != nil {
		return err
	}

	return s.Engine.PublishAndWait(ctx, ev)
}

func buildRoutes(eps []v1.Endpoint, clusters []v1.Cluster, ees []v1.ExternalEndpoint) []Route {
	routeMap := make(map[string]Route, len(eps)+len(ees))
	internalURLs := make(map[string]string, len(eps))

	for i := range eps {
		ep := &eps[i]
		if ep.Metadata == nil || ep.Spec == nil || ep.GetDeletionTimestamp() != "" || (ep.Status != nil && ep.Status.Phase == v1.EndpointPhaseDELETED) {
			continue
		}

		serviceURL := resolveInternalEndpointURL(ep, clusters)
		if serviceURL == "" {
			continue
		}

		upstreamID := "endpoint:" + ep.Metadata.Workspace + "/" + ep.Metadata.Name
		internalURLs[upstreamID] = serviceURL

		mrs := make(map[string]ModelRoute)

		if ep.Status != nil {
			for _, m := range ep.Status.ServedModels {
				mrs[m] = ModelRoute{Targets: []Target{{Upstream: upstreamID, UpstreamModel: m}}}
			}
		}

		if len(mrs) == 0 && ep.Spec.Model != nil && ep.Spec.Model.Name != "" {
			m := ep.Spec.Model.Name
			mrs[m] = ModelRoute{Targets: []Target{{Upstream: upstreamID, UpstreamModel: m}}}
		}

		key := fmt.Sprintf("/workspace/%s/endpoint/%s", ep.Metadata.Workspace, ep.Metadata.Name)
		routeMap[key] = Route{
			Key:         key,
			Workspace:   ep.Metadata.Workspace,
			Kind:        "internal",
			Name:        ep.Metadata.Name,
			Managed:     true,
			ModelRoutes: mrs,
			Upstreams: []Upstream{{
				ID:             upstreamID,
				URL:            serviceURL,
				TargetProtocol: "openai_chat",
				TrustedUsage:   true,
			}},
		}
	}

	for i := range ees {
		addExternalRoute(routeMap, &ees[i], internalURLs)
	}

	out := make([]Route, 0, len(routeMap))
	for _, r := range routeMap {
		out = append(out, r)
	}

	return out
}

func resolveInternalEndpointURL(ep *v1.Endpoint, clusters []v1.Cluster) string {
	for i := range clusters {
		c := &clusters[i]
		if c.Metadata == nil || c.Metadata.Workspace != ep.Metadata.Workspace || c.Metadata.Name != ep.Spec.Cluster {
			continue
		}

		u, err := utils.FormatServiceURL(c, ep)
		if err == nil {
			return u
		}
	}

	return ""
}

func addExternalRoute(routes map[string]Route, ee *v1.ExternalEndpoint, internalURLs map[string]string) {
	if ee.Metadata == nil || ee.Spec == nil || ee.GetDeletionTimestamp() != "" || (ee.Status != nil && ee.Status.Phase == v1.ExternalEndpointPhaseDELETED) {
		return
	}

	key := fmt.Sprintf("/workspace/%s/external-endpoint/%s", ee.Metadata.Workspace, ee.Metadata.Name)
	r := Route{
		Key:         key,
		Workspace:   ee.Metadata.Workspace,
		Kind:        "external",
		Name:        ee.Metadata.Name,
		Managed:     true,
		ModelRoutes: make(map[string]ModelRoute),
	}

	localToGlobal := make(map[string]string, len(ee.Spec.Upstreams))

	for idx, u := range ee.Spec.Upstreams {
		localName := u.Name
		if localName == "" {
			localName = fmt.Sprintf("idx-%d", idx)
		}

		up := Upstream{
			ID:             key + "#" + localName,
			TargetProtocol: "openai_chat",
		}

		if u.Upstream != nil {
			up.URL = u.Upstream.URL
			up.TargetProtocol = protocolFor(up.URL)

			if u.Auth != nil {
				up.AuthType = u.Auth.Type
				up.Credential = u.Auth.Credential
			}
		}

		if u.EndpointRef != nil {
			up.ID = "endpoint:" + ee.Metadata.Workspace + "/" + *u.EndpointRef
			up.URL = internalURLs[up.ID]
			up.TrustedUsage = true
		}

		if up.URL == "" {
			continue
		}

		localToGlobal[localName] = up.ID
		if u.Name != "" {
			localToGlobal[u.Name] = up.ID
		}

		r.Upstreams = append(r.Upstreams, up)

		if len(ee.Spec.ModelRoutes) == 0 {
			for model, target := range u.ModelMapping {
				mr := r.ModelRoutes[model]
				mr.Targets = append(mr.Targets, Target{Upstream: up.ID, UpstreamModel: target})
				r.ModelRoutes[model] = mr
			}
		}
	}

	for _, mr := range ee.Spec.ModelRoutes {
		route := ModelRoute{
			MaxAttempts:         mr.MaxAttempts,
			RetryableConditions: append([]string(nil), mr.RetryableConditions...),
		}

		for _, t := range mr.Targets {
			globalID, ok := localToGlobal[t.Upstream]
			if !ok {
				continue
			}

			route.Targets = append(route.Targets, Target{
				Upstream:      globalID,
				UpstreamModel: t.UpstreamModel,
				Priority:      t.Priority,
				Weight:        t.Weight,
				MaxInflight:   t.MaxInflightRequests,
			})
		}

		r.ModelRoutes[mr.Model] = route
	}

	routes[key] = r
}

func (s *PostgRESTSyncer) buildKeysAndPolicies(keys []v1.ApiKey, groups []v1.GroupPolicy) ([]APIKey, []PolicyGroup) {
	pgMap := make(map[string]PolicyGroup, len(groups))

	for i := range groups {
		g := &groups[i]
		if g.Spec == nil || g.Metadata == nil || g.GetDeletionTimestamp() != "" {
			continue
		}

		pg := PolicyGroup{ID: g.Metadata.Name}
		for _, m := range g.Spec.AllowedModels {
			pg.AllowedModels = append(pg.AllowedModels, GroupModelRule{Model: m.Model, Workspace: m.Workspace})
		}

		for _, m := range g.Spec.DeniedModels {
			pg.DeniedModels = append(pg.DeniedModels, GroupModelRule{Model: m.Model, Workspace: m.Workspace})
		}

		if g.Spec.TokenQuota != nil {
			lim := g.Spec.TokenQuota.Limit
			pg.TokenQuota = &lim
		}

		pgMap[g.Metadata.Name] = pg
	}

	api := make([]APIKey, 0, len(keys))

	for i := range keys {
		k := &keys[i]
		if k.Status == nil || k.GetDeletionTimestamp() != "" || k.Status.Phase == v1.ApiKeyPhaseDELETED || k.Status.SkValue == "" {
			continue
		}

		active, err := s.Storage.ApiKeyOwnerActive(k.ID)
		if err != nil || !active {
			continue
		}

		v := APIKey{
			ID:      k.ID,
			Secret:  k.Status.SkValue,
			OwnerID: k.OwnerID(),
		}

		if k.Metadata != nil {
			v.Workspace = k.Metadata.Workspace
		}

		if k.Spec != nil && k.Spec.Limits != nil {
			l := k.Spec.Limits
			v.Disabled = l.Disabled
			hasModelLimit := false

			if l.AllowedModels != nil {
				v.AllowedModels = make([]AllowedModel, 0, len(l.AllowedModels))

				for _, m := range l.AllowedModels {
					if m.TokenLimit != nil {
						hasModelLimit = true
					}

					v.AllowedModels = append(v.AllowedModels, AllowedModel{
						Model:        m.Model,
						Type:         m.Type,
						EndpointName: m.EndpointName,
						TokenLimit:   m.TokenLimit,
					})
				}
			}

			if !hasModelLimit && l.TokenQuota != nil {
				lim := l.TokenQuota.Limit
				v.TokenQuota = &lim
			}
		}

		for j := range groups {
			g := &groups[j]
			if g.Spec != nil && g.Metadata != nil && g.GetDeletionTimestamp() == "" && g.Spec.SubjectName == v.OwnerID {
				v.PolicyGroupIDs = append(v.PolicyGroupIDs, g.Metadata.Name)
			}
		}

		api = append(api, v)
	}

	policyGroups := make([]PolicyGroup, 0, len(pgMap))
	for _, pg := range pgMap {
		policyGroups = append(policyGroups, pg)
	}

	return api, policyGroups
}

func protocolFor(u string) string {
	u = strings.ToLower(u)
	if strings.Contains(u, "anthropic") || strings.Contains(u, "/v1/messages") {
		return "anthropic_messages"
	}

	if strings.Contains(u, "/v1/responses") {
		return "openai_responses"
	}

	return "openai_chat"
}
