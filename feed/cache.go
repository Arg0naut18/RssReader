package feed

import (
	"net/http"
	"sync"
	"time"
)

const defaultTTL = 15 * time.Minute

type cachedFeed struct {
	feed      *Feed
	fetchedAt time.Time
}

// Cache holds fetched feeds with TTL-based invalidation.
type Cache struct {
	mu      sync.RWMutex
	entries map[string]cachedFeed
	ttl     time.Duration
	client  *http.Client
}

// NewCache creates a Cache with the given TTL (0 → default 15 min).
func NewCache(ttl time.Duration) *Cache {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	return &Cache{
		entries: make(map[string]cachedFeed),
		ttl:     ttl,
		client: &http.Client{
			Timeout: 20 * time.Second,
		},
	}
}

// Get returns a cached feed or fetches it fresh if stale/missing.
func (c *Cache) Get(url string) (*Feed, error) {
	c.mu.RLock()
	entry, ok := c.entries[url]
	c.mu.RUnlock()

	if ok && time.Since(entry.fetchedAt) < c.ttl {
		return entry.feed, nil
	}

	f, err := Fetch(url, c.client)
	if err != nil {
		// Return stale data if available, rather than an error.
		if ok {
			return entry.feed, nil
		}
		return nil, err
	}

	c.mu.Lock()
	c.entries[url] = cachedFeed{feed: f, fetchedAt: time.Now()}
	c.mu.Unlock()

	return f, nil
}

// Invalidate removes a URL from the cache.
func (c *Cache) Invalidate(url string) {
	c.mu.Lock()
	delete(c.entries, url)
	c.mu.Unlock()
}

// Stats returns per-URL cache age info for debugging.
func (c *Cache) Stats() map[string]time.Duration {
	c.mu.RLock()
	defer c.mu.RUnlock()
	out := make(map[string]time.Duration, len(c.entries))
	for u, e := range c.entries {
		out[u] = time.Since(e.fetchedAt)
	}
	return out
}
