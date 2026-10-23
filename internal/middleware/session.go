package middleware

import (
	"fmt"
	"sync"
	"time"

	"github.com/neutree-ai/neutree/pkg/storage"
)

// sessionCacheTTL bounds how long a session check is reused: a session that
// was ended, or whose user was banned, stops being accepted at most this long
// later.
const sessionCacheTTL = 10 * time.Second

// sessionCacheSweepSize is the number of entries above which a store into the
// cache first drops the expired ones.
const sessionCacheSweepSize = 4096

// verifySessionActive refuses a user token whose GoTrue session no longer
// stands. The signature and expiry say who the token is for and how long it
// lasts, not whether the session was ended since (logout) or the user
// banned: GoTrue keeps such a token valid until it expires.
//
// A token without a session_id claim is not a GoTrue session token: GoTrue
// puts the claim in every access token it issues, so only a holder of the JWT
// secret can mint one without it (the service_role token neutree uses
// internally). Such a token is accepted as before.
//
// A failure to check denies the request with errSessionStateUnknown.
func verifySessionActive(store storage.Storage, cache *sessionCache, claims *Claims) error {
	if claims.SessionID == "" {
		return nil
	}

	key := claims.SessionID + "|" + claims.UserID

	active, ok := cache.get(key)
	if !ok {
		if store == nil {
			return fmt.Errorf("%w: no storage configured", errSessionStateUnknown)
		}

		var err error

		active, err = store.IsSessionActive(claims.SessionID, claims.UserID)
		if err != nil {
			return fmt.Errorf("%w: %s", errSessionStateUnknown, err.Error())
		}

		cache.put(key, active)
	}

	if !active {
		return errSessionEnded
	}

	return nil
}

// sessionCache remembers session checks for a short time, so a burst of
// requests with one token costs one database call.
type sessionCache struct {
	ttl time.Duration
	now func() time.Time

	mu      sync.Mutex
	entries map[string]sessionCacheEntry
}

type sessionCacheEntry struct {
	active  bool
	expires time.Time
}

func newSessionCache(ttl time.Duration, now func() time.Time) *sessionCache {
	return &sessionCache{ttl: ttl, now: now, entries: map[string]sessionCacheEntry{}}
}

func (c *sessionCache) get(key string) (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	entry, ok := c.entries[key]
	if !ok || !c.now().Before(entry.expires) {
		return false, false
	}

	return entry.active, true
}

func (c *sessionCache) put(key string, active bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	now := c.now()

	if len(c.entries) >= sessionCacheSweepSize {
		for k, entry := range c.entries {
			if !now.Before(entry.expires) {
				delete(c.entries, k)
			}
		}
	}

	c.entries[key] = sessionCacheEntry{active: active, expires: now.Add(c.ttl)}
}
