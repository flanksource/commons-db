package query

import (
	"time"

	"github.com/patrickmn/go-cache"
)

var (
	// resourceSelectorCache caches the results of resource selector queries
	resourceSelectorCache = cache.New(time.Second*90, time.Minute*5)

	// immutableCache caches immutable resource selector results (no expiration)
	immutableCache = cache.New(cache.NoExpiration, time.Hour*12)
)

// FlushResourceSelectorCache flushes all resource selector caches
func FlushResourceSelectorCache() {
	resourceSelectorCache.Flush()
	immutableCache.Flush()
}
