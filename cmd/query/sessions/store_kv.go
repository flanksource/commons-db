package sessions

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/query"
)

// DefaultSessionTTL is how long a kv session record lives from its start.
const DefaultSessionTTL = 30 * 24 * time.Hour

// kvExpiryMargin keeps List and Begin away from records about to expire, so a
// start and its status never race each other out of the store mid-read.
const kvExpiryMargin = time.Minute

// KVResolver names the cache a request's session records live in and the key
// prefix that isolates its environment (e.g. "oipa.lab:").
type KVResolver func(ctx context.Context) (cache.Store, string, error)

// KVStoreOptions configures a KVStore.
type KVStoreOptions struct {
	// TTL is how long a record lives from its StartedAt; zero is
	// DefaultSessionTTL.
	TTL time.Duration
}

// KVStore is a query.SessionStore over a clicky cache.Store:
//
//	<prefix>sessions:v1:<id>:start   immutable SessionStart JSON
//	<prefix>sessions:v1:<id>:status  SessionStatus JSON, expiry pinned to start
//	<prefix>sessions:v1:index        ZSET id → startedAt (unix ms)
type KVStore struct {
	resolve KVResolver
	ttl     time.Duration
	shared  bool
}

var _ query.SessionStore = (*KVStore)(nil)

// NewKVStore stores records in the cache resolve names for each call; the
// cache is shared with every process using it.
func NewKVStore(resolve KVResolver, options KVStoreOptions) (*KVStore, error) {
	if resolve == nil {
		return nil, errors.New("kv session store: a resolver is required")
	}
	return newKVStore(resolve, options, true)
}

// NewMemoryKVStore stores records in this process's memory for its lifetime.
func NewMemoryKVStore(options KVStoreOptions) (*KVStore, error) {
	memory := cache.NewMemory()
	return newKVStore(func(context.Context) (cache.Store, string, error) { return memory, "", nil }, options, false)
}

func newKVStore(resolve KVResolver, options KVStoreOptions, shared bool) (*KVStore, error) {
	switch {
	case options.TTL < 0:
		return nil, fmt.Errorf("kv session store: ttl %s must not be negative", options.TTL)
	case options.TTL == 0:
		options.TTL = DefaultSessionTTL
	case options.TTL <= kvExpiryMargin:
		return nil, fmt.Errorf("kv session store: ttl %s must exceed %s", options.TTL, kvExpiryMargin)
	}
	return &KVStore{resolve: resolve, ttl: options.TTL, shared: shared}, nil
}

type kvKeys struct {
	kv     cache.Store
	prefix string
}

func (k kvKeys) start(id string) string  { return k.prefix + "sessions:v1:" + id + ":start" }
func (k kvKeys) status(id string) string { return k.prefix + "sessions:v1:" + id + ":status" }
func (k kvKeys) index() string           { return k.prefix + "sessions:v1:index" }

func (s *KVStore) keys(ctx context.Context) (kvKeys, error) {
	kv, prefix, err := s.resolve(ctx)
	if err != nil {
		return kvKeys{}, fmt.Errorf("resolve session store: %w", err)
	}
	if kv == nil {
		return kvKeys{}, errors.New("resolve session store: resolver returned no cache")
	}
	return kvKeys{kv: kv, prefix: prefix}, nil
}

// remaining is how long a record started at startedAt has left to live.
func (s *KVStore) remaining(startedAt time.Time) time.Duration {
	return time.Until(startedAt.Add(s.ttl))
}

func (s *KVStore) Begin(ctx context.Context, rec query.SessionRecord) error {
	if rec.ID == "" || rec.StartedAt.IsZero() {
		return fmt.Errorf("begin session: id %q and startedAt are both required", rec.ID)
	}
	ttl := s.remaining(rec.StartedAt)
	if ttl <= kvExpiryMargin {
		return fmt.Errorf("begin session %s: started %s, outside the %s session ttl", rec.ID, rec.StartedAt.Format(time.RFC3339), s.ttl)
	}
	k, err := s.keys(ctx)
	if err != nil {
		return err
	}
	switch _, err := k.kv.Get(ctx, k.start(rec.ID)); {
	case err == nil:
		return fmt.Errorf("begin session %s: a record already exists", rec.ID)
	case !errors.Is(err, cache.ErrKeyNotFound):
		return fmt.Errorf("begin session %s: read start: %w", rec.ID, err)
	}
	start, err := json.Marshal(rec.SessionStart)
	if err != nil {
		return fmt.Errorf("begin session %s: encode start: %w", rec.ID, err)
	}
	if err := k.kv.Set(ctx, k.start(rec.ID), start, ttl); err != nil {
		return fmt.Errorf("begin session %s: write start: %w", rec.ID, err)
	}
	if err := s.writeStatus(ctx, k, rec.ID, rec.SessionStatus, ttl); err != nil {
		return fmt.Errorf("begin session %s: %w", rec.ID, err)
	}
	if err := k.kv.ZAdd(ctx, k.index(), float64(rec.StartedAt.UnixMilli()), rec.ID); err != nil {
		return fmt.Errorf("begin session %s: index: %w", rec.ID, err)
	}
	cutoff := float64(time.Now().Add(-s.ttl).UnixMilli())
	if err := k.kv.ZRemRangeByScore(ctx, k.index(), cache.NegInf, cache.Exclusive(cutoff)); err != nil {
		return fmt.Errorf("begin session %s: trim index: %w", rec.ID, err)
	}
	return nil
}

func (s *KVStore) writeStatus(ctx context.Context, k kvKeys, id string, status query.SessionStatus, ttl time.Duration) error {
	data, err := json.Marshal(status)
	if err != nil {
		return fmt.Errorf("encode status: %w", err)
	}
	if err := k.kv.Set(ctx, k.status(id), data, ttl); err != nil {
		return fmt.Errorf("write status: %w", err)
	}
	return nil
}

// Update overwrites the status with the expiry its start record has left.
func (s *KVStore) Update(ctx context.Context, id string, status query.SessionStatus) error {
	k, err := s.keys(ctx)
	if err != nil {
		return err
	}
	data, err := k.kv.Get(ctx, k.start(id))
	if errors.Is(err, cache.ErrKeyNotFound) {
		return fmt.Errorf("update session %s: no start record; it was never begun or has expired", id)
	}
	if err != nil {
		return fmt.Errorf("update session %s: read start: %w", id, err)
	}
	var start query.SessionStart
	if err := json.Unmarshal(data, &start); err != nil {
		return fmt.Errorf("update session %s: decode start: %w", id, err)
	}
	ttl := s.remaining(start.StartedAt)
	if ttl <= 0 {
		return fmt.Errorf("update session %s: the record expired at %s", id, start.StartedAt.Add(s.ttl).Format(time.RFC3339))
	}
	if err := s.writeStatus(ctx, k, id, status, ttl); err != nil {
		return fmt.Errorf("update session %s: %w", id, err)
	}
	return nil
}

// Delete removes a persisted session's start, status, and list index entry.
func (s *KVStore) Delete(ctx context.Context, id string) error {
	if id == "" {
		return fmt.Errorf("delete session: id is required")
	}
	k, err := s.keys(ctx)
	if err != nil {
		return err
	}
	if err := k.kv.ZRem(ctx, k.index(), id); err != nil {
		return fmt.Errorf("delete session %s: remove index: %w", id, err)
	}
	if err := k.kv.Del(ctx, k.status(id)); err != nil {
		return fmt.Errorf("delete session %s: remove status: %w", id, err)
	}
	if err := k.kv.Del(ctx, k.start(id)); err != nil {
		return fmt.Errorf("delete session %s: remove start: %w", id, err)
	}
	return nil
}

func (s *KVStore) Get(ctx context.Context, id string) (query.SessionRecord, bool, error) {
	k, err := s.keys(ctx)
	if err != nil {
		return query.SessionRecord{}, false, err
	}
	records, missing, err := s.read(ctx, k, []string{id})
	if err != nil || len(missing) > 0 {
		return query.SessionRecord{}, false, err
	}
	return records[0], true, nil
}

// List reads every record in the filter's startedAt window (bounded by the
// ttl), dropping index entries whose start expired, then filters in Go.
func (s *KVStore) List(ctx context.Context, filter query.SessionFilter) (query.SessionPage, error) {
	if err := filter.Validate(); err != nil {
		return query.SessionPage{}, err
	}
	records, err := s.window(ctx, filter.From, filter.To)
	if err != nil {
		return query.SessionPage{}, err
	}
	page, err := query.ApplySessionFilter(records, filter)
	page.Shared = s.shared
	return page, err
}

// Lineage reads only the records that could name ids: those started no earlier
// than the earliest of them, since a restart always starts after what it
// restarts.
func (s *KVStore) Lineage(ctx context.Context, ids []string) (map[string][]string, error) {
	if len(ids) == 0 {
		return map[string][]string{}, nil
	}
	k, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	parents, _, err := s.read(ctx, k, ids)
	if err != nil {
		return nil, err
	}
	if len(parents) == 0 {
		return map[string][]string{}, nil
	}
	earliest := parents[0].StartedAt
	for _, parent := range parents[1:] {
		if parent.StartedAt.Before(earliest) {
			earliest = parent.StartedAt
		}
	}
	records, err := s.window(ctx, earliest, time.Time{})
	if err != nil {
		return nil, err
	}
	return lineageOf(records, ids), nil
}

// lineageOf maps each of ids to the records restarted from it, in records'
// order.
func lineageOf(records []query.SessionRecord, ids []string) map[string][]string {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}
	lineage := map[string][]string{}
	for _, rec := range records {
		if rec.RestartOf != "" && wanted[rec.RestartOf] {
			lineage[rec.RestartOf] = append(lineage[rec.RestartOf], rec.ID)
		}
	}
	return lineage
}

func (s *KVStore) window(ctx context.Context, from, to time.Time) ([]query.SessionRecord, error) {
	k, err := s.keys(ctx)
	if err != nil {
		return nil, err
	}
	lower := time.Now().Add(-s.ttl + kvExpiryMargin)
	if from.After(lower) {
		lower = from
	}
	upper := cache.PosInf
	if !to.IsZero() {
		upper = cache.Inclusive(float64(to.UnixMilli()))
	}
	ids, err := k.kv.ZRangeByScore(ctx, k.index(), cache.Inclusive(float64(lower.UnixMilli())), upper)
	if err != nil {
		return nil, fmt.Errorf("list sessions: read index: %w", err)
	}
	records, missing, err := s.read(ctx, k, ids)
	if err != nil {
		return nil, err
	}
	for _, id := range missing {
		if err := k.kv.ZRem(ctx, k.index(), id); err != nil {
			return nil, fmt.Errorf("list sessions: drop expired %s from the index: %w", id, err)
		}
	}
	return records, nil
}
