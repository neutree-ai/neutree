package state

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/neutree-ai/neutree/internal/icp/quota"
)

var ErrEngineClosed = errors.New("engine closed")

type EventKind string

const (
	EventConfig  EventKind = "config"
	EventQuota   EventKind = "quota"
	EventRuntime EventKind = "runtime"
)

type Event struct {
	Kind    EventKind          `json:"kind"`
	Config  *ConfigEvent       `json:"config,omitempty"`
	Quota   *quota.QuotaUpdate `json:"quota,omitempty"`
	Runtime *RuntimeEvent      `json:"runtime,omitempty"`
	ack     chan struct{}
}

type Upstream struct {
	ID             string `json:"id"`
	URL            string `json:"url"`
	TargetProtocol string `json:"target_protocol"`
	AuthType       string `json:"auth_type,omitempty"`
	Credential     string `json:"credential,omitempty"`
	MaxInflight    int    `json:"max_inflight,omitempty"`
	TrustedUsage   bool   `json:"trusted_usage,omitempty"`
}

func (u Upstream) IsTrustedUsage(routeKind string) bool {
	return u.TrustedUsage || routeKind == "internal" || strings.HasPrefix(u.ID, "endpoint:")
}

type Target struct {
	Upstream      string `json:"upstream"`
	UpstreamModel string `json:"upstream_model"`
	Priority      int    `json:"priority,omitempty"`
	Weight        int    `json:"weight,omitempty"`
	MaxInflight   int    `json:"max_inflight,omitempty"`
}

type ModelRoute struct {
	MaxAttempts         int      `json:"max_attempts,omitempty"`
	RetryableConditions []string `json:"retryable_conditions,omitempty"`
	Targets             []Target `json:"targets"`
}

type Route struct {
	Key         string                `json:"key"`
	Workspace   string                `json:"workspace"`
	Kind        string                `json:"kind"`
	Name        string                `json:"name"`
	Managed     bool                  `json:"managed"`
	Upstreams   []Upstream            `json:"upstreams"`
	ModelRoutes map[string]ModelRoute `json:"model_routes,omitempty"`
	SubPath     string                `json:"sub_path,omitempty"`
}

type AllowedModel struct {
	Model        string `json:"model"`
	Type         string `json:"type,omitempty"`
	EndpointName string `json:"endpoint_name,omitempty"`
	TokenLimit   *int64 `json:"token_limit,omitempty"`
}

type GroupModelRule struct {
	Model     string `json:"model"`
	Workspace string `json:"workspace,omitempty"`
}

type APIKey struct {
	ID             string         `json:"id"`
	Secret         string         `json:"secret,omitempty"`
	Workspace      string         `json:"workspace"`
	OwnerID        string         `json:"owner_id,omitempty"`
	PolicyGroupIDs []string       `json:"policy_group_ids,omitempty"`
	AllowedModels  []AllowedModel `json:"allowed_models"`
	Disabled       bool           `json:"disabled,omitempty"`
	TokenQuota     *int64         `json:"token_quota,omitempty"`
}

type PolicyGroup struct {
	ID            string           `json:"id"`
	AllowedModels []GroupModelRule `json:"allowed_models,omitempty"`
	DeniedModels  []GroupModelRule `json:"denied_models,omitempty"`
	TokenQuota    *int64           `json:"token_quota,omitempty"`
}

type ConfigEvent struct {
	Replace        bool            `json:"replace,omitempty"`
	LegacyFallback *bool           `json:"legacy_fallback,omitempty"`
	ManagedRoutes  map[string]bool `json:"managed_routes,omitempty"`
	APIKeys        []APIKey        `json:"api_keys,omitempty"`
	PolicyGroups   []PolicyGroup   `json:"policy_groups,omitempty"`
	Routes         []Route         `json:"routes,omitempty"`
}

type ConfigSnapshot struct {
	Version               uint64                 `json:"version"`
	LegacyFallbackEnabled bool                   `json:"legacy_fallback_enabled"`
	ManagedRoutes         map[string]bool        `json:"managed_routes,omitempty"`
	APIKeys               map[string]APIKey      `json:"api_keys"`
	Secrets               map[string]string      `json:"-"`
	PolicyGroups          map[string]PolicyGroup `json:"policy_groups"`
	Routes                map[string]Route       `json:"routes"`
}

type RuntimeEvent struct {
	UpstreamID   string    `json:"upstream_id"`
	Healthy      *bool     `json:"healthy,omitempty"`
	QueueDepth   int       `json:"queue_depth,omitempty"`
	PrefixHashes []string  `json:"prefix_hashes,omitempty"`
	ExpiresAt    time.Time `json:"expires_at"`
}

type RuntimeState struct {
	Healthy      bool      `json:"healthy"`
	QueueDepth   int       `json:"queue_depth"`
	InFlight     int       `json:"in_flight"`
	PrefixHashes []string  `json:"prefix_hashes,omitempty"`
	ExpiresAt    time.Time `json:"expires_at,omitempty"`
}

type RuntimeTable struct {
	mu     sync.RWMutex
	values map[string]RuntimeState
}

func NewRuntimeTable() *RuntimeTable {
	return &RuntimeTable{values: make(map[string]RuntimeState)}
}

func (t *RuntimeTable) Update(e RuntimeEvent) {
	t.mu.Lock()
	defer t.mu.Unlock()

	v, ok := t.values[e.UpstreamID]
	if !ok {
		v.Healthy = true
	}

	if e.Healthy != nil {
		v.Healthy = *e.Healthy
	}

	v.QueueDepth = e.QueueDepth
	v.PrefixHashes = append([]string(nil), e.PrefixHashes...)
	v.ExpiresAt = e.ExpiresAt
	t.values[e.UpstreamID] = v
}

func (t *RuntimeTable) AcquireInFlight(id string) func() {
	t.mu.Lock()

	v, ok := t.values[id]
	if !ok {
		v.Healthy = true
	}

	v.InFlight++
	t.values[id] = v
	t.mu.Unlock()

	return func() {
		t.ReleaseInFlight(id)
	}
}

func (t *RuntimeTable) ReleaseInFlight(id string) {
	t.mu.Lock()

	v, ok := t.values[id]
	if !ok {
		v.Healthy = true
	}

	if v.InFlight > 0 {
		v.InFlight--
	}

	t.values[id] = v
	t.mu.Unlock()
}

func (t *RuntimeTable) Get(id string) RuntimeState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	v, ok := t.values[id]
	if !ok {
		return RuntimeState{Healthy: true}
	}

	if !v.ExpiresAt.IsZero() && time.Now().After(v.ExpiresAt) {
		return RuntimeState{Healthy: true, InFlight: v.InFlight}
	}

	return v
}

func (t *RuntimeTable) Snapshot() map[string]RuntimeState {
	t.mu.RLock()
	defer t.mu.RUnlock()

	now := time.Now()
	out := make(map[string]RuntimeState, len(t.values))

	for id, v := range t.values {
		if !v.ExpiresAt.IsZero() && now.After(v.ExpiresAt) {
			out[id] = RuntimeState{Healthy: true, InFlight: v.InFlight}
			continue
		}

		out[id] = v
	}

	return out
}

type Engine struct {
	config  atomic.Pointer[ConfigSnapshot]
	runtime *RuntimeTable
	leases  *quota.LeasePool
	events  chan Event
	done    chan struct{}
}

func NewEngine(initial *ConfigSnapshot, leases *quota.LeasePool, queue int) *Engine {
	if initial == nil {
		initial = emptySnapshot()
	}

	if queue <= 0 {
		queue = 64
	}

	e := &Engine{
		runtime: NewRuntimeTable(),
		leases:  leases,
		events:  make(chan Event, queue),
		done:    make(chan struct{}),
	}
	e.config.Store(initial)

	if leases != nil && (len(initial.APIKeys) > 0 || len(initial.PolicyGroups) > 0) {
		initEvent := &ConfigEvent{
			APIKeys:      make([]APIKey, 0, len(initial.APIKeys)),
			PolicyGroups: make([]PolicyGroup, 0, len(initial.PolicyGroups)),
		}

		for _, k := range initial.APIKeys {
			initEvent.APIKeys = append(initEvent.APIKeys, k)
		}

		for _, g := range initial.PolicyGroups {
			initEvent.PolicyGroups = append(initEvent.PolicyGroups, g)
		}

		e.syncQuotaLimits(initEvent)
	}

	return e
}

func emptySnapshot() *ConfigSnapshot {
	return &ConfigSnapshot{
		APIKeys:       make(map[string]APIKey),
		Secrets:       make(map[string]string),
		PolicyGroups:  make(map[string]PolicyGroup),
		Routes:        make(map[string]Route),
		ManagedRoutes: make(map[string]bool),
	}
}

func (e *Engine) Snapshot() *ConfigSnapshot {
	return e.config.Load()
}

func (e *Engine) Runtime(id string) RuntimeState {
	return e.runtime.Get(id)
}

func (e *Engine) RuntimeTable() *RuntimeTable {
	return e.runtime
}

func (e *Engine) Publish(v Event) error {
	select {
	case e.events <- v:
		return nil
	case <-e.done:
		return ErrEngineClosed
	}
}

func (e *Engine) PublishAndWait(ctx context.Context, v Event) error {
	ack := make(chan struct{})
	v.ack = ack

	select {
	case e.events <- v:
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return ErrEngineClosed
	}

	select {
	case <-ack:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	case <-e.done:
		return ErrEngineClosed
	}
}

func (e *Engine) Drain() {
	for {
		select {
		case v := <-e.events:
			e.apply(v)
		default:
			return
		}
	}
}

func (e *Engine) Run() {
	for {
		select {
		case v := <-e.events:
			e.apply(v)
		case <-e.done:
			return
		}
	}
}

func (e *Engine) Close() {
	select {
	case <-e.done:
	default:
		close(e.done)
	}
}

func (e *Engine) apply(v Event) {
	switch v.Kind {
	case EventConfig:
		e.applyConfig(v.Config)
	case EventQuota:
		if e.leases != nil && v.Quota != nil {
			e.leases.Configure(*v.Quota)
		}
	case EventRuntime:
		if v.Runtime != nil {
			e.runtime.Update(*v.Runtime)
		}
	}

	if v.ack != nil {
		close(v.ack)
	}
}

func (e *Engine) applyConfig(c *ConfigEvent) {
	if c == nil {
		return
	}

	old := e.Snapshot()
	n := cloneSnapshot(old)

	if c.Replace {
		n = emptySnapshot()
		n.LegacyFallbackEnabled = old.LegacyFallbackEnabled

		for k, v := range old.ManagedRoutes {
			n.ManagedRoutes[k] = v
		}
	}

	if c.LegacyFallback != nil {
		n.LegacyFallbackEnabled = *c.LegacyFallback
	}

	for k, v := range c.ManagedRoutes {
		if v {
			n.ManagedRoutes[k] = true
		} else {
			delete(n.ManagedRoutes, k)
		}
	}

	for _, v := range c.APIKeys {
		if oldv, ok := n.APIKeys[v.ID]; ok && oldv.Secret != "" {
			delete(n.Secrets, oldv.Secret)
		}

		n.APIKeys[v.ID] = v

		if v.Secret != "" {
			n.Secrets[v.Secret] = v.ID
		}
	}

	for _, v := range c.PolicyGroups {
		n.PolicyGroups[v.ID] = v
	}

	for _, v := range c.Routes {
		n.Routes[v.Key] = v
	}

	n.Version = old.Version + 1
	e.config.Store(n)
	e.syncQuotaLimits(c)
}

func (e *Engine) syncQuotaLimits(c *ConfigEvent) {
	if e.leases == nil {
		return
	}

	var zero int64

	for _, key := range c.APIKeys {
		hasModelLimit := false

		for _, m := range key.AllowedModels {
			if m.TokenLimit != nil {
				hasModelLimit = true
				lim := *m.TokenLimit
				e.leases.Configure(quota.QuotaUpdate{
					PoolID: quota.KeyModelPoolID(key.ID, AllowedRuleID(m)),
					Limit:  &lim,
				})
			}
		}

		if hasModelLimit || key.TokenQuota == nil {
			e.leases.Configure(quota.QuotaUpdate{PoolID: quota.KeyPoolID(key.ID), Limit: &zero})
		} else {
			lim := *key.TokenQuota
			e.leases.Configure(quota.QuotaUpdate{PoolID: quota.KeyPoolID(key.ID), Limit: &lim})
		}
	}

	for _, group := range c.PolicyGroups {
		if group.TokenQuota == nil {
			e.leases.Configure(quota.QuotaUpdate{PoolID: quota.GroupPoolID(group.ID), Limit: &zero})
		} else {
			lim := *group.TokenQuota
			e.leases.Configure(quota.QuotaUpdate{PoolID: quota.GroupPoolID(group.ID), Limit: &lim})
		}
	}
}

func cloneSnapshot(s *ConfigSnapshot) *ConfigSnapshot {
	n := emptySnapshot()
	n.Version = s.Version
	n.LegacyFallbackEnabled = s.LegacyFallbackEnabled

	for k, v := range s.ManagedRoutes {
		n.ManagedRoutes[k] = v
	}

	for k, v := range s.APIKeys {
		n.APIKeys[k] = v
	}

	for k, v := range s.Secrets {
		n.Secrets[k] = v
	}

	for k, v := range s.PolicyGroups {
		n.PolicyGroups[k] = v
	}

	for k, v := range s.Routes {
		n.Routes[k] = v
	}

	return n
}

func ExtractAPIKey(r *http.Request, s *ConfigSnapshot) (APIKey, bool) {
	raw := strings.TrimSpace(r.Header.Get("x-api-key"))
	if raw == "" {
		raw = strings.TrimSpace(r.Header.Get("kong_apikey"))
	}

	if raw == "" {
		a := r.Header.Get("Authorization")
		if len(a) > 7 && strings.EqualFold(a[:7], "bearer ") {
			raw = strings.TrimSpace(a[7:])
		}
	}

	if raw == "" {
		return APIKey{}, false
	}

	id, ok := s.Secrets[raw]
	if !ok {
		return APIKey{}, false
	}

	k, ok := s.APIKeys[id]

	return k, ok && !k.Disabled
}
