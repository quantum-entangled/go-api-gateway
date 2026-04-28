package cache

import (
	"container/list"
	"sync"
	"time"
)

// Entry is a cached HTTP response captured by the cache middleware.
type Entry struct {
	Status    int
	Header    map[string][]string
	Body      []byte
	ETag      string
	ExpiresAt time.Time
}

// Size returns the entry's contribution to the cache's byte budget. Only
// Body bytes are counted. Header overhead is treated as negligible.
func (e *Entry) Size() int {
	if e == nil {
		return 0
	}
	return len(e.Body)
}

type item struct {
	key   string
	entry *Entry
}

// LRU is a fixed-capacity in-memory cache with TTL-based expiry.
// Stale entries are evicted lazily on Get or under capacity pressure.
// Untouched entries past their ExpiresAt may linger in memory until
// either condition triggers. Safe for concurrent use.
type LRU struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	bytes      int
	order      *list.List
	index      map[string]*list.Element
}

// NewLRU constructs a cache bounded by maxEntries and maxBytes. A zero value
// for either bound disables that bound (use both >0 in practice).
func NewLRU(maxEntries, maxBytes int) *LRU {
	return &LRU{
		maxEntries: maxEntries,
		maxBytes:   maxBytes,
		order:      list.New(),
		index:      make(map[string]*list.Element),
	}
}

// Get returns the entry for key if present and unexpired, marking it as
// most-recently used. Expired entries are evicted on access.
func (c *LRU) Get(key string) (*Entry, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	el, ok := c.index[key]
	if !ok {
		return nil, false
	}
	it := el.Value.(*item)
	if time.Now().After(it.entry.ExpiresAt) {
		c.removeElement(el)
		return nil, false
	}
	c.order.MoveToFront(el)
	return it.entry, true
}

// Set inserts or replaces the entry for key. Eviction runs until the cache
// fits within both bounds.
func (c *LRU) Set(key string, entry *Entry) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.index[key]; ok {
		c.removeElement(el)
	}

	it := &item{key: key, entry: entry}
	el := c.order.PushFront(it)
	c.index[key] = el
	c.bytes += entry.Size()

	c.evict()
}

func (c *LRU) Len() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.order.Len()
}

func (c *LRU) Bytes() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.bytes
}

func (c *LRU) evict() {
	for c.maxEntries > 0 && c.order.Len() > c.maxEntries {
		c.removeElement(c.order.Back())
	}
	for c.maxBytes > 0 && c.bytes > c.maxBytes {
		back := c.order.Back()
		if back == nil {
			return
		}
		c.removeElement(back)
	}
}

func (c *LRU) removeElement(el *list.Element) {
	it := el.Value.(*item)
	c.order.Remove(el)
	delete(c.index, it.key)
	c.bytes -= it.entry.Size()
}
