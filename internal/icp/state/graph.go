package state

import (
	"strings"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

func HasModelRestrictions(s *ConfigSnapshot, key APIKey) bool {
	if key.AllowedModels != nil {
		return true
	}

	if s == nil {
		return false
	}

	for _, id := range key.PolicyGroupIDs {
		if g, ok := s.PolicyGroups[id]; ok && (len(g.AllowedModels) > 0 || len(g.DeniedModels) > 0) {
			return true
		}
	}

	return false
}

func Allowed(s *ConfigSnapshot, key APIKey, workspace, model, kind, endpoint string) bool {
	if key.Disabled {
		return false
	}

	if key.Workspace != "" && workspace != "" && key.Workspace != workspace {
		return false
	}

	for _, m := range key.AllowedModels {
		if m.Model == model && (m.Type == "" || m.Type == kind) && (m.EndpointName == "" || m.EndpointName == endpoint) {
			return groupAllowed(s, key, workspace, model)
		}
	}

	if key.AllowedModels != nil {
		return false
	}

	return groupAllowed(s, key, workspace, model)
}

func AllowedRuleID(m AllowedModel) string {
	return m.Model + "|" + m.Type + "|" + m.EndpointName
}

func PoolIDsForRequest(s *ConfigSnapshot, key APIKey, model, kind, endpoint string) []string {
	var pools []string
	hasModelLimit := false

	for _, m := range key.AllowedModels {
		if m.TokenLimit != nil {
			hasModelLimit = true
		}

		if m.TokenLimit != nil && m.Model == model && (m.Type == "" || m.Type == kind) && (m.EndpointName == "" || m.EndpointName == endpoint) {
			pools = append(pools, quota.KeyModelPoolID(key.ID, AllowedRuleID(m)))
			break
		}
	}

	if !hasModelLimit && key.TokenQuota != nil {
		pools = append(pools, quota.KeyPoolID(key.ID))
	}

	for _, gid := range key.PolicyGroupIDs {
		if s != nil {
			if g, ok := s.PolicyGroups[gid]; ok && g.TokenQuota != nil {
				pools = append(pools, quota.GroupPoolID(gid))
			}
		}
	}

	return pools
}

func groupAllowed(s *ConfigSnapshot, key APIKey, workspace, model string) bool {
	for _, id := range key.PolicyGroupIDs {
		g, ok := s.PolicyGroups[id]
		if !ok {
			continue
		}

		for _, d := range g.DeniedModels {
			if d.Model == model && (d.Workspace == "" || d.Workspace == workspace) {
				return false
			}
		}

		if len(g.AllowedModels) > 0 {
			matched := false

			for _, a := range g.AllowedModels {
				if a.Model == model && (a.Workspace == "" || a.Workspace == workspace) {
					matched = true
					break
				}
			}

			if !matched {
				return false
			}
		}
	}

	return true
}

func (s *ConfigSnapshot) MatchRoute(path string) (Route, bool) {
	var best Route
	matched := false

	for _, r := range s.Routes {
		if !r.Managed {
			continue
		}

		if path != r.Key && !strings.HasPrefix(path, r.Key+"/") {
			continue
		}

		if s.LegacyFallbackEnabled && !s.ManagedRoutes["*"] && !s.ManagedRoutes[r.Key] {
			continue
		}

		if !matched || len(r.Key) > len(best.Key) {
			best = r
			matched = true
		}
	}

	if !matched {
		return Route{}, false
	}

	best.SubPath = strings.TrimPrefix(path, best.Key)
	if best.SubPath == "" {
		best.SubPath = "/"
	}

	return best, true
}
