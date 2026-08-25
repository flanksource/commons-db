package inspect

import (
	"container/list"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
	"time"
)

const DefaultFillTimeout = 15 * time.Second

type CacheState string

const (
	CacheStateFresh CacheState = "fresh"
	CacheStateStale CacheState = "stale"
)

type CachePolicy struct {
	Name            string
	InitialFreshFor time.Duration
	MaximumFreshFor time.Duration
	FillTimeout     time.Duration
	MaxEntries      int
	MaxWeight       int
}

type CacheMetadata struct {
	Policy             string     `json:"policy"`
	State              CacheState `json:"state"`
	Cached             bool       `json:"cached"`
	Refreshing         bool       `json:"refreshing,omitempty"`
	LoadedAt           time.Time  `json:"loadedAt"`
	FreshUntil         time.Time  `json:"freshUntil"`
	LastChangedAt      time.Time  `json:"lastChangedAt"`
	LastRefreshAttempt *time.Time `json:"lastRefreshAttempt,omitempty"`
	LastRefreshError   string     `json:"lastRefreshError,omitempty"`
	AgeMS              int64      `json:"ageMs"`
	RetryAfterMS       int64      `json:"retryAfterMs,omitempty"`
	UnchangedRefreshes int        `json:"unchangedRefreshes,omitempty"`
}

type Result[T any] struct {
	Value T
	Cache CacheMetadata
}

type GetOptions[T any] struct {
	Key     string
	Refresh bool
	Load    func(context.Context) (T, error)
}

type MemoOptions[T any] struct {
	Policy      CachePolicy
	Weight      func(T) int
	Fingerprint func(T) (string, error)
	Now         func() time.Time
}

type memoEntry[T any] struct {
	value              T
	fingerprint        string
	loadedAt           time.Time
	freshUntil         time.Time
	lastChangedAt      time.Time
	lastAttemptAt      time.Time
	nextAttemptAt      time.Time
	lastRefreshError   string
	unchangedRefreshes int
	failures           int
	weight             int
	element            *list.Element
}

type flightKey struct {
	key        string
	generation uint64
}

type memoFlight[T any] struct {
	done       chan struct{}
	result     Result[T]
	err        error
	manual     bool
	generation uint64
}

type Memo[T any] struct {
	mu          sync.Mutex
	policy      CachePolicy
	weight      func(T) int
	fingerprint func(T) (string, error)
	now         func() time.Time
	entries     map[string]*memoEntry[T]
	generations map[string]uint64
	flights     map[flightKey]*memoFlight[T]
	lru         *list.List
	totalWeight int
}

func NewMemo[T any](options MemoOptions[T]) *Memo[T] {
	policy := options.Policy
	if strings.TrimSpace(policy.Name) == "" {
		panic("inspection cache policy name is required")
	}
	if policy.InitialFreshFor <= 0 {
		panic(fmt.Sprintf("inspection cache policy %q initial freshness must be positive", policy.Name))
	}
	if policy.MaximumFreshFor < policy.InitialFreshFor {
		panic(fmt.Sprintf("inspection cache policy %q maximum freshness must be at least the initial freshness", policy.Name))
	}
	if policy.FillTimeout <= 0 {
		policy.FillTimeout = DefaultFillTimeout
	}
	if policy.MaxEntries <= 0 || policy.MaxWeight <= 0 {
		panic(fmt.Sprintf("inspection cache policy %q requires positive entry and weight limits", policy.Name))
	}
	weight := options.Weight
	if weight == nil {
		weight = func(T) int { return 1 }
	}
	fingerprint := options.Fingerprint
	if fingerprint == nil {
		fingerprint = jsonFingerprint[T]
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	memo := &Memo[T]{
		policy: policy, weight: weight, fingerprint: fingerprint, now: now,
		entries: map[string]*memoEntry[T]{}, generations: map[string]uint64{},
		flights: map[flightKey]*memoFlight[T]{}, lru: list.New(),
	}
	// Registered here rather than by each call site, so a cache added later is
	// reachable for flushing without anyone remembering to say so.
	Register(memo)
	return memo
}

// Policy is the freshness and capacity contract this memo was built with.
func (m *Memo[T]) Policy() CachePolicy { return m.policy }

// Stats describes what the memo currently holds.
func (m *Memo[T]) Stats() CacheStats {
	m.mu.Lock()
	defer m.mu.Unlock()
	stats := CacheStats{
		Policy: m.policy.Name, Entries: len(m.entries), MaxEntries: m.policy.MaxEntries,
		Weight: m.totalWeight, MaxWeight: m.policy.MaxWeight, Filling: len(m.flights),
		FreshForSeconds:    int64(m.policy.InitialFreshFor.Seconds()),
		MaxFreshForSeconds: int64(m.policy.MaximumFreshFor.Seconds()),
	}
	// The LRU's back is the least recently *used* entry, which is the one whose
	// load is furthest from mattering — the honest answer to "how old is the
	// oldest thing in here".
	if oldest := m.lru.Back(); oldest != nil {
		if entry := m.entries[oldest.Value.(string)]; entry != nil {
			stats.Oldest = entry.loadedAt
		}
	}
	return stats
}

// Clear drops every entry and returns how many there were.
//
// Generations are bumped for entries being filled right now, so a fill already
// in flight cannot land afterwards and resurrect what was just dropped — which
// is the difference between a flush and a suggestion.
func (m *Memo[T]) Clear() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	dropped := len(m.entries)
	keys := make([]string, 0, len(m.entries))
	for key := range m.entries {
		keys = append(keys, key)
	}
	for identifier := range m.flights {
		keys = append(keys, identifier.key)
	}
	for _, key := range keys {
		m.generations[key]++
		m.removeLocked(key)
	}
	return dropped
}

func (m *Memo[T]) Get(ctx context.Context, options GetOptions[T]) (Result[T], error) {
	if strings.TrimSpace(options.Key) == "" {
		return Result[T]{}, fmt.Errorf("inspection cache key is required")
	}
	if options.Load == nil {
		return Result[T]{}, fmt.Errorf("inspection cache loader is required")
	}
	// Timed and reported here rather than inside the branches below so that a
	// hit and a miss are measured the same way — the whole point of watching
	// this cache is comparing the two.
	started := m.now()
	result, err := m.get(ctx, options)
	observe(ctx, Observation{
		Policy: m.policy.Name, Key: options.Key,
		Elapsed: m.now().Sub(started), Cache: result.Cache, Err: err,
	})
	return result, err
}

func (m *Memo[T]) get(ctx context.Context, options GetOptions[T]) (Result[T], error) {
	// A refresh asked for on the context applies to every lookup the request
	// makes, which is what "re-run inspection" means: one request pays to rebuild
	// what it reads, rather than an operator flushing the cache out from under
	// everyone else to answer a question about their own page.
	if options.Refresh || RefreshRequested(ctx) {
		return m.refresh(ctx, options)
	}

	m.mu.Lock()
	entry := m.entries[options.Key]
	if entry != nil {
		m.lru.MoveToFront(entry.element)
		now := m.now()
		if !now.Before(entry.freshUntil) && !now.Before(entry.nextAttemptAt) {
			m.startLocked(ctx, options.Key, m.generations[options.Key], false, options.Load)
		}
		result := m.resultLocked(options.Key, entry, now)
		m.mu.Unlock()
		return result, nil
	}
	flight := m.startLocked(ctx, options.Key, m.generations[options.Key], false, options.Load)
	m.mu.Unlock()
	return waitForFlight(ctx, flight)
}

func (m *Memo[T]) refresh(ctx context.Context, options GetOptions[T]) (Result[T], error) {
	m.mu.Lock()
	currentGeneration := m.generations[options.Key]
	if current := m.flights[flightKey{key: options.Key, generation: currentGeneration}]; current != nil && current.manual {
		m.mu.Unlock()
		return waitForFlight(ctx, current)
	}
	generation := currentGeneration + 1
	m.generations[options.Key] = generation
	flight := m.startLocked(ctx, options.Key, generation, true, options.Load)
	m.mu.Unlock()
	return waitForFlight(ctx, flight)
}

func (m *Memo[T]) startLocked(
	ctx context.Context,
	key string,
	generation uint64,
	manual bool,
	load func(context.Context) (T, error),
) *memoFlight[T] {
	identifier := flightKey{key: key, generation: generation}
	if current := m.flights[identifier]; current != nil {
		return current
	}
	flight := &memoFlight[T]{done: make(chan struct{}), manual: manual, generation: generation}
	m.flights[identifier] = flight
	go m.runFill(context.WithoutCancel(ctx), identifier, flight, load)
	return flight
}

func (m *Memo[T]) runFill(
	parent context.Context,
	identifier flightKey,
	flight *memoFlight[T],
	load func(context.Context) (T, error),
) {
	ctx, cancel := context.WithTimeout(parent, m.policy.FillTimeout)
	defer cancel()
	started := m.now()
	value, err := load(ctx)
	finished := m.now()
	var fingerprint string
	if err == nil {
		fingerprint, err = m.fingerprint(value)
		if err != nil {
			err = fmt.Errorf("fingerprint inspection metadata: %w", err)
		}
	}

	m.mu.Lock()
	defer m.mu.Unlock()
	defer close(flight.done)
	delete(m.flights, identifier)
	if err != nil {
		m.recordFailureLocked(identifier.key, identifier.generation, finished, err, flight.manual)
		if entry := m.entries[identifier.key]; entry != nil {
			flight.result = m.resultLocked(identifier.key, entry, finished)
		} else if !m.hasFlightLocked(identifier.key) {
			delete(m.generations, identifier.key)
		}
		flight.err = err
		return
	}
	if m.generations[identifier.key] != identifier.generation {
		if entry := m.entries[identifier.key]; entry != nil {
			flight.result = m.resultLocked(identifier.key, entry, finished)
		} else {
			flight.result = Result[T]{Value: value}
		}
		return
	}

	previous := m.entries[identifier.key]
	unchangedRefreshes := 0
	lastChangedAt := finished
	if previous != nil && previous.fingerprint == fingerprint {
		unchangedRefreshes = previous.unchangedRefreshes + 1
		lastChangedAt = previous.lastChangedAt
	}
	freshFor := adaptiveFreshFor(m.policy, finished.Sub(started), unchangedRefreshes)
	entry := &memoEntry[T]{
		value: value, fingerprint: fingerprint, loadedAt: finished,
		freshUntil: finished.Add(freshFor), lastChangedAt: lastChangedAt,
		lastAttemptAt: finished, unchangedRefreshes: unchangedRefreshes,
		weight: max(1, m.weight(value)),
	}
	m.storeLocked(identifier.key, entry)
	flight.result = m.resultLocked(identifier.key, entry, finished)
	flight.result.Cache.Cached = false
}

func (m *Memo[T]) recordFailureLocked(key string, generation uint64, now time.Time, err error, manual bool) {
	if m.generations[key] != generation {
		return
	}
	entry := m.entries[key]
	if entry == nil {
		return
	}
	entry.failures++
	entry.lastAttemptAt = now
	entry.lastRefreshError = err.Error()
	entry.nextAttemptAt = now.Add(refreshBackoff(entry.failures))
	if manual && entry.freshUntil.After(now) {
		entry.freshUntil = now
	}
}

func (m *Memo[T]) storeLocked(key string, entry *memoEntry[T]) {
	if previous := m.entries[key]; previous != nil {
		m.totalWeight -= previous.weight
		m.lru.Remove(previous.element)
	}
	entry.element = m.lru.PushFront(key)
	m.entries[key] = entry
	m.totalWeight += entry.weight
	for len(m.entries) > m.policy.MaxEntries || m.totalWeight > m.policy.MaxWeight {
		oldest := m.lru.Back()
		if oldest == nil {
			break
		}
		m.removeLocked(oldest.Value.(string))
	}
}

func (m *Memo[T]) resultLocked(key string, entry *memoEntry[T], now time.Time) Result[T] {
	state := CacheStateFresh
	if !now.Before(entry.freshUntil) {
		state = CacheStateStale
	}
	lastAttempt := entry.lastAttemptAt
	metadata := CacheMetadata{
		Policy: m.policy.Name, State: state, Cached: true,
		Refreshing: m.flights[flightKey{key: key, generation: m.generations[key]}] != nil,
		LoadedAt:   entry.loadedAt, FreshUntil: entry.freshUntil,
		LastChangedAt: entry.lastChangedAt, UnchangedRefreshes: entry.unchangedRefreshes,
		LastRefreshError: entry.lastRefreshError,
		AgeMS:            max(int64(0), now.Sub(entry.loadedAt).Milliseconds()),
	}
	if !lastAttempt.IsZero() {
		metadata.LastRefreshAttempt = &lastAttempt
	}
	if now.Before(entry.nextAttemptAt) {
		metadata.RetryAfterMS = entry.nextAttemptAt.Sub(now).Milliseconds()
	}
	return Result[T]{Value: entry.value, Cache: metadata}
}

func (m *Memo[T]) Invalidate(key string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.generations[key]++
	m.removeLocked(key)
}

func (m *Memo[T]) InvalidatePrefix(prefix string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	keys := map[string]struct{}{}
	for key := range m.entries {
		if strings.HasPrefix(key, prefix) {
			keys[key] = struct{}{}
		}
	}
	for identifier := range m.flights {
		if strings.HasPrefix(identifier.key, prefix) {
			keys[identifier.key] = struct{}{}
		}
	}
	for key := range keys {
		m.generations[key]++
		m.removeLocked(key)
	}
}

func (m *Memo[T]) removeLocked(key string) {
	entry := m.entries[key]
	if entry == nil {
		return
	}
	delete(m.entries, key)
	m.lru.Remove(entry.element)
	m.totalWeight -= entry.weight
	if !m.hasFlightLocked(key) {
		delete(m.generations, key)
	}
}

func (m *Memo[T]) hasFlightLocked(key string) bool {
	for identifier := range m.flights {
		if identifier.key == key {
			return true
		}
	}
	return false
}

func waitForFlight[T any](ctx context.Context, flight *memoFlight[T]) (Result[T], error) {
	select {
	case <-ctx.Done():
		return Result[T]{}, ctx.Err()
	case <-flight.done:
		return flight.result, flight.err
	}
}

func adaptiveFreshFor(policy CachePolicy, elapsed time.Duration, unchangedRefreshes int) time.Duration {
	costMultiplier := time.Duration(1)
	if elapsed > 5*time.Second {
		costMultiplier = 4
	} else if elapsed >= time.Second {
		costMultiplier = 2
	}
	stabilityMultiplier := time.Duration(1)
	if unchangedRefreshes >= 2 {
		stabilityMultiplier = 4
	} else if unchangedRefreshes == 1 {
		stabilityMultiplier = 2
	}
	freshFor := policy.InitialFreshFor * costMultiplier * stabilityMultiplier
	if freshFor > policy.MaximumFreshFor {
		return policy.MaximumFreshFor
	}
	return freshFor
}

func refreshBackoff(failures int) time.Duration {
	backoffs := [...]time.Duration{5 * time.Minute, 30 * time.Minute, 2 * time.Hour, 6 * time.Hour}
	return backoffs[min(max(1, failures), len(backoffs))-1]
}

func jsonFingerprint[T any](value T) (string, error) {
	encoded, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(encoded)), nil
}
