package model_registry

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"sync"
	"time"

	v1 "github.com/neutree-ai/neutree/api/v1"
	"github.com/neutree-ai/neutree/internal/util"
)

const (
	// DefaultQueryCacheTTL is how long a public registry's answer to a query is
	// reused, so that browsing a hub costs one round of queries rather than one
	// per interaction. Retry-connection invalidation, not this window, is what
	// bounds staleness after a user fixes something.
	DefaultQueryCacheTTL = 5 * time.Minute
	// DefaultQueryCacheMaxEntries bounds the cache. The search term is user input,
	// so the key space is unbounded and cannot be left to expiry alone.
	DefaultQueryCacheMaxEntries = 512
)

// QueryCache holds recent listing results for public registries. Private
// registries are never cached: they are local filesystems, and a model pushed a
// moment ago has to appear at once.
//
// The cache is per process, so with several API replicas an invalidation only
// reaches the replica that served the request; the rest correct themselves
// within the TTL.
//
// A nil *QueryCache is usable and caches nothing.
type QueryCache struct {
	ttl        time.Duration
	maxEntries int
	// now overrides the clock in tests.
	now func() time.Time

	mu      sync.Mutex
	entries map[string]cacheEntry
}

type cacheEntry struct {
	// scope is the key prefix the entry belongs to, kept so an invalidation can
	// find every entry of one registry without parsing keys back apart.
	scope     string
	page      *ModelPage
	fetchedAt time.Time
	expiresAt time.Time
}

// QueryMeta says when an answer was read from the registry and whether it was
// reused. A cached listing describes the hub as of FetchedAt, not as of the
// request, and a caller cannot work that out for itself.
type QueryMeta struct {
	// FetchedAt is when this data was actually read from the registry.
	FetchedAt time.Time
	// Cached says the answer was reused rather than fetched for this request.
	Cached bool
}

// NewQueryCache builds a cache. A ttl of zero or less means
// DefaultQueryCacheTTL.
func NewQueryCache(ttl time.Duration) *QueryCache {
	if ttl <= 0 {
		ttl = DefaultQueryCacheTTL
	}

	return &QueryCache{
		ttl:        ttl,
		maxEntries: DefaultQueryCacheMaxEntries,
		entries:    map[string]cacheEntry{},
	}
}

func (c *QueryCache) clock() time.Time {
	if c == nil || c.now == nil {
		return time.Now()
	}

	return c.now()
}

// ListModels answers from the cache when it can, and otherwise asks the registry
// and records the answer. Errors are never cached, so a hub that refuses one
// request is asked again on the next.
func (c *QueryCache) ListModels(registry *v1.ModelRegistry, client ModelRegistry,
	option ListOption) (*ModelPage, QueryMeta, error) {
	if !c.cacheable(registry) {
		page, err := client.ListModels(option)
		if err != nil {
			return nil, QueryMeta{}, err
		}

		return page, QueryMeta{FetchedAt: c.clock()}, nil
	}

	scope := registryScope(registry)
	key := scope + "|" + queryKey(option)

	if page, fetchedAt, ok := c.get(key); ok {
		return page, QueryMeta{FetchedAt: fetchedAt, Cached: true}, nil
	}

	page, err := client.ListModels(option)
	if err != nil {
		return nil, QueryMeta{}, err
	}

	fetchedAt := c.clock()
	c.put(scope, key, page, fetchedAt)

	return page, QueryMeta{FetchedAt: fetchedAt}, nil
}

// Invalidate drops everything cached for one registry. Retry-connection calls it
// so that a refreshed status is not shown next to results fetched before the fix.
func (c *QueryCache) Invalidate(registry *v1.ModelRegistry) {
	if c == nil || registry == nil {
		return
	}

	scope := registryScope(registry)

	c.mu.Lock()
	defer c.mu.Unlock()

	for key, entry := range c.entries {
		if entry.scope == scope {
			delete(c.entries, key)
		}
	}
}

// cacheable reports whether this registry's listings may be reused.
func (c *QueryCache) cacheable(registry *v1.ModelRegistry) bool {
	if c == nil || registry == nil || registry.Spec == nil {
		return false
	}

	return v1.VisibilityForModelRegistryType(registry.Spec.Type) == v1.ModelRegistryVisibilityPublic
}

func (c *QueryCache) get(key string) (*ModelPage, time.Time, bool) {
	c.mu.Lock()
	entry, ok := c.entries[key]

	if ok && !c.clock().Before(entry.expiresAt) {
		delete(c.entries, key)

		ok = false
	}
	c.mu.Unlock()

	if !ok {
		return nil, time.Time{}, false
	}

	// A copy, because the list handler decorates what it gets with aliases; a
	// shared page would carry one request's decorations into the next.
	page, err := util.DeepCopyObject(entry.page)
	if err != nil {
		return nil, time.Time{}, false
	}

	return page, entry.fetchedAt, true
}

func (c *QueryCache) put(scope, key string, page *ModelPage, fetchedAt time.Time) {
	stored, err := util.DeepCopyObject(page)
	if err != nil {
		return
	}

	c.mu.Lock()
	defer c.mu.Unlock()

	if len(c.entries) >= c.maxEntries {
		c.evictLocked()
	}

	c.entries[key] = cacheEntry{
		scope:     scope,
		page:      stored,
		fetchedAt: fetchedAt,
		expiresAt: fetchedAt.Add(c.ttl),
	}
}

// evictLocked makes room: expired entries first, otherwise the one closest to
// expiring. Entries are few and short-lived, so an exact LRU would buy nothing.
func (c *QueryCache) evictLocked() {
	now := c.clock()
	evicted := false

	for key, entry := range c.entries {
		if !now.Before(entry.expiresAt) {
			delete(c.entries, key)

			evicted = true
		}
	}

	if evicted {
		return
	}

	var (
		oldestKey string
		oldest    time.Time
	)

	for key, entry := range c.entries {
		if oldestKey == "" || entry.expiresAt.Before(oldest) {
			oldestKey, oldest = key, entry.expiresAt
		}
	}

	delete(c.entries, oldestKey)
}

// registryScope identifies whose results these are. The credential is part of
// the key because a hub shows a different catalogue to different callers — gated
// repositories appear only for a token that may see them — so an answer fetched
// with one credential must never be served under another. The URL is in the key
// so that repointing a registry at another mirror starts from an empty cache.
//
// The credential is reduced to a digest; the value itself is not needed here.
//
// This depends on ModelRegistrySpec.Credentials still being populated on a
// storage read. The api:"-" tag on it is applied by the client-facing resource
// proxy, not by storage. If that ever changes, every scope fingerprints as
// "anonymous" and callers start seeing each other's results, with this function
// unchanged.
func registryScope(registry *v1.ModelRegistry) string {
	var (
		workspace string
		name      string
		url       string
		token     string
	)

	if registry.Metadata != nil {
		workspace = registry.Metadata.Workspace
		name = registry.Metadata.Name
	}

	if registry.Spec != nil {
		url = registry.Spec.Url
		token = registry.Spec.Credentials
	}

	return fmt.Sprintf("%s/%s/%d/%s/%s", workspace, name, registry.ID, url, credentialFingerprint(token))
}

func credentialFingerprint(credentials string) string {
	if credentials == "" {
		return "anonymous"
	}

	sum := sha256.Sum256([]byte(credentials))

	return hex.EncodeToString(sum[:8])
}

func queryKey(option ListOption) string {
	return fmt.Sprintf("search=%s&offset=%d&limit=%d", option.Search, option.Offset, option.Limit)
}
