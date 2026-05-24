package browser

import (
	"sync"

	"github.com/nickna/ssh.night.ms/internal/reader"
)

// cacheCap bounds the per-session article cache. 16 is generous for normal
// reading flow and keeps the memory footprint trivial — each article is a
// handful of KB plus the block slice.
const cacheCap = 16

// Cache is a tiny URL→Article LRU. Eviction is by access order; Get bumps
// the URL to the front. Safe for concurrent use.
type Cache struct {
	mu    sync.Mutex
	store map[string]*reader.Article
	order []string // MRU at index 0
}

// NewCache returns an empty cache.
func NewCache() *Cache {
	return &Cache{store: make(map[string]*reader.Article)}
}

// Get returns the cached article for url and marks it most-recently-used.
func (c *Cache) Get(url string) (*reader.Article, bool) {
	if c == nil || url == "" {
		return nil, false
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	a, ok := c.store[url]
	if !ok {
		return nil, false
	}
	c.bumpLocked(url)
	return a, true
}

// Put inserts or replaces the cached article for url, then trims to cacheCap.
func (c *Cache) Put(url string, a *reader.Article) {
	if c == nil || url == "" || a == nil {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, exists := c.store[url]; exists {
		c.store[url] = a
		c.bumpLocked(url)
		return
	}
	c.store[url] = a
	c.order = append([]string{url}, c.order...)
	for len(c.order) > cacheCap {
		victim := c.order[len(c.order)-1]
		c.order = c.order[:len(c.order)-1]
		delete(c.store, victim)
	}
}

// Forget evicts a single URL (used by `r` reload so a fresh fetch isn't
// short-circuited by stale cache).
func (c *Cache) Forget(url string) {
	if c == nil || url == "" {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if _, ok := c.store[url]; !ok {
		return
	}
	delete(c.store, url)
	for i, u := range c.order {
		if u == url {
			c.order = append(c.order[:i], c.order[i+1:]...)
			break
		}
	}
}

func (c *Cache) bumpLocked(url string) {
	for i, u := range c.order {
		if u != url {
			continue
		}
		if i == 0 {
			return
		}
		c.order = append([]string{url}, append(c.order[:i], c.order[i+1:]...)...)
		return
	}
}
