package sessionstoretest

import (
	"context"
	"iter"
	"strings"
	"sync"

	"github.com/flanksource/clicky/cache"
)

// CountingCache counts how often each session's start key is read, so a spec
// can see which records a request read and how many times.
type CountingCache struct {
	cache.Store
	mu    sync.Mutex
	reads map[string]int
}

func NewCountingCache(store cache.Store) *CountingCache {
	return &CountingCache{Store: store, reads: map[string]int{}}
}

func (c *CountingCache) MGet(ctx context.Context, keys iter.Seq[string]) iter.Seq2[cache.Entry, error] {
	c.mu.Lock()
	for key := range keys {
		if strings.HasSuffix(key, ":start") {
			c.reads[key]++
		}
	}
	c.mu.Unlock()
	return c.Store.MGet(ctx, keys)
}

func (c *CountingCache) Get(ctx context.Context, key string) ([]byte, error) {
	c.mu.Lock()
	if strings.HasSuffix(key, ":start") {
		c.reads[key]++
	}
	c.mu.Unlock()
	return c.Store.Get(ctx, key)
}

// ReadsOf is how often session id's start key was read under any prefix.
func (c *CountingCache) ReadsOf(id string) int {
	c.mu.Lock()
	defer c.mu.Unlock()
	total := 0
	for key, reads := range c.reads {
		if strings.HasSuffix(key, "sessions:v1:"+id+":start") {
			total += reads
		}
	}
	return total
}

func (c *CountingCache) Reset() {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.reads = map[string]int{}
}
