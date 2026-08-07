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
	// reused.
	//
	// Long enough that browsing a hub — searching, paging back and forth,
	// opening something and returning to the list — is one round of queries
	// rather than one per interaction, and short enough that the catalogue is
	// never meaningfully behind. Staleness within the window is bounded in the
	// one place it matters: "retry connection" drops the entries, so a user who
	// has just fixed something never has to wait it out.
	DefaultQueryCacheTTL = 5 * time.Minute
	// DefaultQueryCacheMaxEntries bounds the cache. The search term is user input,
	// so the number of distinct keys is unbounded and the cache needs a ceiling
	// that does not depend on entries expiring.
	DefaultQueryCacheMaxEntries = 512
)

// QueryCache holds recent listing results for public registries.
//
// Only public registries are cached. A private registry is a local filesystem —
// listing it is cheap, and a model pushed a second ago should show up — whereas
// a public one is an HTTP call to somebody else's service that returns the same
// answer to the same question for as long as anyone cares.
//
// The cache lives in the process, so a deployment running several API replicas
// keeps one per replica. That is visible in exactly one way: an invalidation
// reaches the replica that served the request. It self-corrects within the TTL,
// and the alternative — a shared cache — would be a new piece of infrastructure
// for entries that expire in minutes.
//
// A nil *QueryCache is usable and caches nothing, so callers that have no cache
// configured need no special case.
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

// QueryMeta says where an answer came from and how old it is.
//
// A listing served from cache is a statement about the hub at the moment it was
// fetched, not at the moment it was asked for. Handing that back without saying
// so leaves a caller unable to tell a catalogue that has not changed from one it
// is being shown a five-minute-old copy of — so the age travels with the data
// rather than being inferred from it.
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
// and records the answer. Errors are never cached: a hub that is refusing
// requests now may well answer the next one, and a cached failure would outlive
// the condition that caused it.
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

// Invalidate drops everything cached for one registry. It is what makes "retry
// connection" mean something to a user looking at a stale page: without it the
// retry would refresh the registry's status while the listing next to it still
// showed the results, or the emptiness, from before.
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

	// Hand back a copy. The caller decorates what it gets — aliases are attached
	// onto the returned models — and a cache that let one request's decorations
	// show up in the next one would be worse than no cache at all.
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

// evictLocked makes room: expired entries first, and if there were none, the one
// closest to expiring. Entries are short-lived and few, so an exact LRU would
// buy nothing over this scan.
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

// registryScope identifies whose results these are.
//
// The workspace and the credential are both part of it, and both for the same
// reason: a public hub shows a different catalogue to different callers — gated
// and private repositories appear only for a token that may see them — so an
// answer fetched with one credential must never be served to another. The URL is
// in there too, so that repointing a registry at a different mirror starts from
// an empty cache rather than from the old mirror's answers.
//
// The credential is reduced to a digest. Nothing here needs the value itself,
// and a map key is the kind of thing that ends up in a heap dump.
//
// This rests on ModelRegistrySpec.Credentials still holding a value here.
// The field is tagged api:"-", but that tag is applied by the client-facing
// resource proxy on its way out to a caller, not by the storage read this
// registry object came from — newHuggingFace already depends on the same thing
// for its token. Were that ever to change, every scope would fingerprint as
// "anonymous" and one caller's view of a gated repository would be served to
// another, with this function still reading exactly as it does now.
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
