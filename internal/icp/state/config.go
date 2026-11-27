package state

import (
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"strings"
	"sync/atomic"
	"unicode"

	"github.com/neutree-ai/neutree/internal/icp/event"
)

// PGDeltaSource replays committed deltas in revision order within each lane.
// ConfigReplace carries ConfigSnapshot. RouteUpsert carries Route, RouteDelete
// only its key. QuotaLevel carries the absolute int64 balance for the key.
// Publishing configuration and admission for Kong are independent in stage 1.
const (
	ConfigReplace event.Kind = "config.snapshot"
	RouteUpsert   event.Kind = "route.upsert"
	RouteDelete   event.Kind = "route.delete"
	QuotaLevel    event.Kind = "quota.level"
)

type Route struct {
	UpstreamProtocol string                                `json:"upstream_protocol,omitempty"`
	RoutingLogic     string                                `json:"routing_logic"`
	ModelDefaults    map[string]map[string]json.RawMessage `json:"model_defaults"`
}

type ConfigSnapshot struct {
	Kong   string           `json:"kong"`
	Routes map[string]Route `json:"routes"` // client-facing /workspace/{ws}/{type}/{name}
	Quotas map[string]int64 `json:"quotas"`
}

type Store struct {
	snapshot atomic.Pointer[ConfigSnapshot]
}

func (s *Store) Config() *ConfigSnapshot { return s.snapshot.Load() }
func normalizeRoute(r Route) (Route, error) {
	if r.RoutingLogic == "" {
		r.RoutingLogic = "roundrobin"
	}

	if id := r.UpstreamProtocol; strings.TrimSpace(id) != id || strings.IndexFunc(id, unicode.IsControl) >= 0 {
		return r, fmt.Errorf("invalid upstream protocol %q", id)
	}

	for model, params := range r.ModelDefaults {
		if model == "" {
			return r, errors.New("empty default model key")
		}

		for key, v := range params {
			switch strings.ToLower(key) {
			case "model", "stream", "messages", "input", "stream_options", "conversation", "previous_response_id", "background", "store", "tools":
				return r, fmt.Errorf("reserved model default %q", key)
			}

			if !json.Valid(v) {
				return r, fmt.Errorf("invalid default %q", key)
			}
		}
	}

	return r, nil
}

// Apply takes ownership of config payloads. A rejected event never replaces the live snapshot.
func (s *Store) Apply(e event.Event) error {
	var next ConfigSnapshot
	if cur := s.Config(); cur != nil {
		next = *cur
	}

	switch e.Kind {
	case ConfigReplace:
		cfg, ok := e.Value.(ConfigSnapshot)
		if !ok {
			return errors.New("expected config snapshot")
		}

		if cfg.Kong == "" {
			cfg.Kong = "http://kong:8000"
		}

		u, err := url.Parse(cfg.Kong)
		if err != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.RawQuery != "" || u.Fragment != "" {
			return errors.New("invalid Kong URL")
		}

		cfg.Kong = strings.TrimRight(cfg.Kong, "/")
		cfg.Routes = maps.Clone(cfg.Routes)

		for k, r := range cfg.Routes {
			r, err = normalizeRoute(r)
			if err != nil {
				return err
			}

			cfg.Routes[k] = r
		}

		next = cfg
	case RouteUpsert, RouteDelete:
		if s.Config() == nil {
			return errors.New("missing initial config")
		}

		next.Routes = maps.Clone(next.Routes)
		if e.Kind == RouteDelete {
			delete(next.Routes, e.Key)
		} else {
			r, ok := e.Value.(Route)
			if !ok {
				return errors.New("expected route")
			}

			r, err := normalizeRoute(r)
			if err != nil {
				return err
			}

			if next.Routes == nil {
				next.Routes = make(map[string]Route)
			}

			next.Routes[e.Key] = r
		}
	case QuotaLevel:
		if s.Config() == nil {
			return errors.New("missing initial config")
		}

		level, ok := e.Value.(int64)
		if !ok {
			return errors.New("expected quota level")
		}

		next.Quotas = maps.Clone(next.Quotas)
		if next.Quotas == nil {
			next.Quotas = make(map[string]int64)
		}

		next.Quotas[e.Key] = level
	default:
		return fmt.Errorf("unknown config event %q", e.Kind)
	}

	s.snapshot.Store(&next)

	return nil
}
