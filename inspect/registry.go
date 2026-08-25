package inspect

// Reaching the caches from outside the packages that own them.
//
// Every Memo is a package-level variable in whichever package needs it —
// OpenSearch field mappings in inspect/opensearch, column cardinality in
// query/providers, SQL catalogs in the connection browser. That is the right
// place for them and the wrong place for an operator: "the field list for this
// index is stale, drop it" has no seam to say it through, and the only remedy
// today is restarting the process.
//
// So a Memo registers itself, and this is the non-generic face a handler can
// hold. The registry is append-only and never removes: the production memos are
// created once at init or once per server, so it does not grow with traffic.

import (
	"sort"
	"sync"
	"time"
)

// Cache is what a Memo looks like to something that does not know its value
// type — enough to describe it and to throw it away.
type Cache interface {
	Policy() CachePolicy
	Stats() CacheStats
	// Clear drops every entry and returns how many there were. Entries being
	// filled right now are invalidated too, so the fill in flight cannot land
	// afterwards and resurrect what was just dropped.
	Clear() int
	Invalidate(key string)
	InvalidatePrefix(prefix string)
}

// CacheStats describes one cache's occupancy against the ceilings its policy
// set — the two numbers that say whether it is doing anything and whether it is
// about to start evicting.
type CacheStats struct {
	Policy     string `json:"policy"`
	Entries    int    `json:"entries"`
	MaxEntries int    `json:"maxEntries"`
	Weight     int    `json:"weight"`
	MaxWeight  int    `json:"maxWeight"`
	// Filling counts loads in flight. A cache that is always filling is one
	// whose freshness window is shorter than the thing it caches takes to build.
	Filling int `json:"filling"`
	// Oldest is when the least recently loaded entry was filled, and is zero for
	// an empty cache.
	Oldest time.Time `json:"oldest,omitempty"`
	// FreshFor and MaxFreshFor are the policy's window, reported so a console can
	// explain a stale read without a second lookup.
	FreshForSeconds    int64 `json:"freshForSeconds"`
	MaxFreshForSeconds int64 `json:"maxFreshForSeconds"`
}

var registry struct {
	mu     sync.Mutex
	caches []Cache
}

// Register adds a cache to the process-wide list. NewMemo calls it, so a memo
// is reachable the moment it exists and no call site has to remember.
func Register(cache Cache) {
	if cache == nil {
		return
	}
	registry.mu.Lock()
	defer registry.mu.Unlock()
	registry.caches = append(registry.caches, cache)
}

// Caches returns the registered caches, ordered by policy name so a console
// renders them in a stable order rather than in registration order, which
// depends on which packages happened to be linked in.
func Caches() []Cache {
	registry.mu.Lock()
	caches := append([]Cache(nil), registry.caches...)
	registry.mu.Unlock()
	sort.SliceStable(caches, func(i, j int) bool {
		return caches[i].Policy().Name < caches[j].Policy().Name
	})
	return caches
}

// Stats describes every registered cache.
func Stats() []CacheStats {
	caches := Caches()
	stats := make([]CacheStats, 0, len(caches))
	for _, cache := range caches {
		stats = append(stats, cache.Stats())
	}
	return stats
}

// FlushOptions narrows what a flush throws away. An empty Policy means every
// cache; an empty Key and Prefix mean every entry within the caches chosen.
type FlushOptions struct {
	Policy string
	Key    string
	Prefix string
}

// FlushResult reports what was actually dropped, per cache.
//
// Reported rather than assumed: a flush aimed at a key that was never cached
// looks identical to one that worked, and an operator who cannot tell the two
// apart will conclude the flush is broken.
type FlushResult struct {
	Caches  []FlushedCache `json:"caches"`
	Entries int            `json:"entries"`
}

type FlushedCache struct {
	Policy  string `json:"policy"`
	Entries int    `json:"entries"`
}

// Flush drops matching entries and reports what went.
func Flush(options FlushOptions) FlushResult {
	result := FlushResult{Caches: []FlushedCache{}}
	for _, cache := range Caches() {
		policy := cache.Policy().Name
		if options.Policy != "" && options.Policy != policy {
			continue
		}
		before := cache.Stats().Entries
		switch {
		case options.Key != "":
			cache.Invalidate(options.Key)
		case options.Prefix != "":
			cache.InvalidatePrefix(options.Prefix)
		default:
			cache.Clear()
		}
		dropped := before - cache.Stats().Entries
		if dropped <= 0 {
			continue
		}
		result.Caches = append(result.Caches, FlushedCache{Policy: policy, Entries: dropped})
		result.Entries += dropped
	}
	return result
}

// PolicyNames lists the cache classes a caller may flush by name, so a UI can
// offer them instead of asking someone to type one.
func PolicyNames() []string {
	seen := map[string]struct{}{}
	names := make([]string, 0)
	for _, cache := range Caches() {
		name := cache.Policy().Name
		if _, duplicate := seen[name]; duplicate {
			continue
		}
		seen[name] = struct{}{}
		names = append(names, name)
	}
	return names
}
