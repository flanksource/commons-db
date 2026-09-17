package sessions

import (
	"context"
	"encoding/json"
	"fmt"
	"iter"
	"slices"
	"sync"
	"sync/atomic"
	"time"

	"github.com/flanksource/clicky/cache"
	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/query"
)

// kvOrphanedRecords counts start records the kv store found without a status
// and removed.
var kvOrphanedRecords atomic.Int64

// KVOrphanedRecords counts the start records any KVStore in this process found
// without their status — evicted under maxmemory, say — and removed, with their
// index entries, rather than failing every list that reached them.
func KVOrphanedRecords() int64 { return kvOrphanedRecords.Load() }

// kvReadMemo holds the records one request has already read, so the list, its
// lineage and its lookups read each record's keys at most once.
type kvReadMemo struct {
	mu      sync.Mutex
	entries map[string]kvMemoEntry // prefix + id
}

// kvMemoEntry is what reading one id found: its record, or that its start is
// gone, or neither — a start whose status is not written yet.
type kvMemoEntry struct {
	record    *query.SessionRecord
	startGone bool
}

type kvReadMemoKey struct{}

// withSessionReadMemo returns ctx carrying a fresh read memo. A handler scopes
// it to one resolution: a record read again later, such as a stream's final
// status, must be read fresh.
func withSessionReadMemo(ctx context.Context) context.Context {
	return context.WithValue(ctx, kvReadMemoKey{}, &kvReadMemo{entries: map[string]kvMemoEntry{}})
}

func readMemo(ctx context.Context) *kvReadMemo {
	memo, _ := ctx.Value(kvReadMemoKey{}).(*kvReadMemo)
	return memo
}

// held returns the entries of ids the memo holds. A nil memo holds none.
func (m *kvReadMemo) held(prefix string, ids []string) map[string]kvMemoEntry {
	held := map[string]kvMemoEntry{}
	if m == nil {
		return held
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for _, id := range ids {
		if entry, ok := m.entries[prefix+id]; ok {
			held[id] = entry
		}
	}
	return held
}

func (m *kvReadMemo) keep(prefix string, entries map[string]kvMemoEntry) {
	if m == nil {
		return
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	for id, entry := range entries {
		m.entries[prefix+id] = entry
	}
}

// read loads the records of ids in order, returning the ids whose start record
// is gone. Records ctx's memo holds are not read again.
func (s *KVStore) read(ctx context.Context, k kvKeys, ids []string) ([]query.SessionRecord, []string, error) {
	memo := readMemo(ctx)
	entries := memo.held(k.prefix, ids)
	unread := slices.DeleteFunc(slices.Clone(ids), func(id string) bool { _, ok := entries[id]; return ok })
	fetched, err := s.fetch(ctx, k, unread)
	if err != nil {
		return nil, nil, err
	}
	memo.keep(k.prefix, fetched)
	records := make([]query.SessionRecord, 0, len(ids))
	var missing []string
	for _, id := range ids {
		entry, ok := entries[id]
		if !ok {
			entry = fetched[id]
		}
		switch {
		case entry.record != nil:
			records = append(records, *entry.record)
		case entry.startGone:
			missing = append(missing, id)
		}
	}
	return records, missing, nil
}

// fetch reads ids' start and status keys in one MGet. A start without its
// status is an orphan once it is older than a Begin's two writes could be
// apart: it is removed and reported missing.
func (s *KVStore) fetch(ctx context.Context, k kvKeys, ids []string) (map[string]kvMemoEntry, error) {
	keys := func(yield func(string) bool) {
		for _, id := range ids {
			if !yield(k.start(id)) || !yield(k.status(id)) {
				return
			}
		}
	}
	next, stop := iter.Pull2(k.kv.MGet(ctx, keys))
	defer stop()
	entries := make(map[string]kvMemoEntry, len(ids))
	for _, id := range ids {
		start, err := pullEntry(next, id)
		if err != nil {
			return nil, err
		}
		status, err := pullEntry(next, id)
		if err != nil {
			return nil, err
		}
		switch {
		case !start.Found:
			entries[id] = kvMemoEntry{startGone: true}
		case !status.Found:
			if err := s.dropOrphan(ctx, k, id, start.Value); err != nil {
				return nil, err
			}
			entries[id] = kvMemoEntry{}
		default:
			rec, err := decodeKVRecord(id, start.Value, status.Value)
			if err != nil {
				return nil, err
			}
			entries[id] = kvMemoEntry{record: &rec}
		}
	}
	return entries, nil
}

// dropOrphan removes a start record whose status is gone, and its index entry.
// A start younger than kvExpiryMargin may be a Begin between its two writes and
// is left alone; either way the record is not listed.
func (s *KVStore) dropOrphan(ctx context.Context, k kvKeys, id string, start []byte) error {
	var rec query.SessionStart
	if err := json.Unmarshal(start, &rec); err != nil {
		return fmt.Errorf("decode session %s start: %w", id, err)
	}
	if time.Since(rec.StartedAt) < kvExpiryMargin {
		return nil
	}
	logger.Errorf("session %s (%s): start record has no status, likely evicted; removing it from the session store", id, rec.Profile)
	if err := k.kv.Del(ctx, k.start(id)); err != nil {
		return fmt.Errorf("session %s: remove start record orphaned by its missing status: %w", id, err)
	}
	if err := k.kv.ZRem(ctx, k.index(), id); err != nil {
		return fmt.Errorf("session %s: remove orphaned record from the index: %w", id, err)
	}
	kvOrphanedRecords.Add(1)
	return nil
}

func pullEntry(next func() (cache.Entry, error, bool), id string) (cache.Entry, error) {
	entry, err, ok := next()
	switch {
	case err != nil:
		return cache.Entry{}, fmt.Errorf("read session %s: %w", id, err)
	case !ok:
		return cache.Entry{}, fmt.Errorf("read session %s: the cache returned fewer entries than keys", id)
	}
	return entry, nil
}

func decodeKVRecord(id string, start, status []byte) (query.SessionRecord, error) {
	var rec query.SessionRecord
	if err := json.Unmarshal(start, &rec.SessionStart); err != nil {
		return rec, fmt.Errorf("decode session %s start: %w", id, err)
	}
	if err := json.Unmarshal(status, &rec.SessionStatus); err != nil {
		return rec, fmt.Errorf("decode session %s status: %w", id, err)
	}
	if rec.ID != id {
		return rec, fmt.Errorf("session %s: start record names id %q", id, rec.ID)
	}
	return rec, nil
}
