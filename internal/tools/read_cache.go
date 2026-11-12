package tools

import (
	"container/list"
	"crypto/sha256"
	"sync"

	"github.com/andrepato/harness/internal/msg"
	"github.com/andrepato/harness/internal/tool"
)

// ReadCache holds bounded, already formatted whole-file text results. A cache
// hit requires freshly read bytes with the same digest; the cache never
// substitutes for a filesystem read or a permission check. Windowed reads
// and images are deliberately excluded by ReadToolWithCache.
type ReadCache struct {
	mu         sync.Mutex
	maxEntries int
	maxBytes   int
	usedBytes  int
	order      *list.List
	items      map[readCacheKey]*list.Element
}

type readCacheKey struct {
	path   string
	digest [sha256.Size]byte
}

type readCacheEntry struct {
	key    readCacheKey
	result tool.Result
	size   int
}

// NewReadCache creates a cache with entry and stored-output byte limits.
// Nonpositive limits disable storage. Both limits are enforced on every put.
func NewReadCache(maxEntries, maxBytes int) *ReadCache {
	return &ReadCache{maxEntries: maxEntries, maxBytes: maxBytes, order: list.New(), items: make(map[readCacheKey]*list.Element)}
}

func (c *ReadCache) lookup(path string, data []byte) (tool.Result, bool) {
	key := readCacheKey{path: path, digest: sha256.Sum256(data)}
	c.mu.Lock()
	defer c.mu.Unlock()
	elem, ok := c.items[key]
	if !ok {
		return tool.Result{}, false
	}
	c.order.MoveToFront(elem)
	return cloneReadResult(elem.Value.(readCacheEntry).result), true
}

func (c *ReadCache) store(path string, data []byte, result tool.Result) {
	if c.maxEntries <= 0 || c.maxBytes <= 0 || len(result.Content) != 1 {
		return
	}
	block, ok := result.Content[0].(msg.TextContent)
	if !ok {
		return
	}
	key := readCacheKey{path: path, digest: sha256.Sum256(data)}
	size := len(path) + len(block.Text) + len(result.Details) + sha256.Size
	if size > c.maxBytes {
		return
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	if elem, exists := c.items[key]; exists {
		c.order.MoveToFront(elem)
		return
	}
	entry := readCacheEntry{key: key, result: cloneReadResult(result), size: size}
	c.items[key] = c.order.PushFront(entry)
	c.usedBytes += size
	for len(c.items) > c.maxEntries || c.usedBytes > c.maxBytes {
		back := c.order.Back()
		old := back.Value.(readCacheEntry)
		delete(c.items, old.key)
		c.usedBytes -= old.size
		c.order.Remove(back)
	}
}

func cloneReadResult(result tool.Result) tool.Result {
	clone := result
	clone.Content = append(msg.Blocks(nil), result.Content...)
	clone.Details = append([]byte(nil), result.Details...)
	return clone
}
