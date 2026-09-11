// Package kv stores record streams in a clicky cache.Store, so one
// implementation runs in process (cache.NewMemory) and against valkey/redis
// (clicky/valkey.NewStore) — the backend a CLI writes to and a server reads.
//
// A stream is three kinds of key under <prefix>/<stream>/:
//
//	meta             the stream's recordstore.Meta as JSON
//	index            a sorted set of chunk names, scored by the chunk's first seq
//	chunk/<first>    one append's rows as a JSON array, first zero-padded
//
// Each append writes whole new chunks and never rewrites one, so a stream of
// ten thousand rows costs ten thousand row encodings rather than a growing blob
// rewritten per append. Every key carries the stream's expiry, so the stream
// leaves the store in one piece.
package kv

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/recordstore"
)

// chunkNameWidth zero-pads a chunk's first seq so a key listing sorts in seq
// order too, not only the index.
const chunkNameWidth = 19

// Options configure a kv backend.
type Options struct {
	// Store holds the keys. It is owned by the caller and not closed.
	Store cache.Store

	// Prefix namespaces every key the backend writes.
	Prefix string

	// TTL is how long a stream lives from its first append unless Expire moves
	// it. It is required: the store is memory, and a stream nobody expires is
	// a leak.
	TTL time.Duration

	// MaxChunkBytes caps one stored chunk. An append is split into as many
	// chunks as it needs; a single row larger than the cap is refused with
	// recordstore.ErrCapacity.
	MaxChunkBytes int

	// Now is the clock stream metadata is stamped with. Nil is time.Now.
	Now func() time.Time
}

// Backend is a recordstore.Backend over a cache.Store.
type Backend struct {
	store         cache.Store
	prefix        string
	ttl           time.Duration
	maxChunkBytes int
	now           func() time.Time
	locks         recordstore.StreamLocks
}

var _ recordstore.Backend = (*Backend)(nil)

// New validates options and returns the backend.
func New(options Options) (*Backend, error) {
	switch {
	case options.Store == nil:
		return nil, fmt.Errorf("kv record store: a cache store is required")
	case strings.TrimSpace(options.Prefix) == "":
		return nil, fmt.Errorf("kv record store: a key prefix is required")
	case options.TTL <= 0:
		return nil, fmt.Errorf("kv record store: a positive stream ttl is required")
	case options.MaxChunkBytes <= 0:
		return nil, fmt.Errorf("kv record store: a positive chunk cap is required")
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Backend{
		store: options.Store, prefix: strings.TrimRight(options.Prefix, "/"), ttl: options.TTL,
		maxChunkBytes: options.MaxChunkBytes, now: now,
	}, nil
}

func (b *Backend) key(stream, suffix string) string { return b.prefix + "/" + stream + "/" + suffix }

func chunkName(first int64) string { return fmt.Sprintf("%0*d", chunkNameWidth, first) }

// Append writes rows as new chunks, then the index entries naming them, then
// the metadata that makes them visible. A reader bounds itself by the
// metadata's high seq, so a crash part way leaves chunks past it that no reader
// sees; the next append removes them before numbering its own rows over the
// same seqs.
func (b *Backend) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.Window, error) {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return recordstore.Window{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	now := b.now()
	meta, err := b.openStream(ctx, stream, kind, now)
	if err != nil {
		return recordstore.Window{}, err
	}
	if err := b.pruneUncommitted(ctx, stream, meta.HighSeq); err != nil {
		return recordstore.Window{}, err
	}
	window := recordstore.Window{From: meta.HighSeq + 1, To: meta.HighSeq + int64(len(rows))}
	chunks, err := splitChunks(rows, window.From, b.maxChunkBytes)
	if err != nil {
		meta.Capped = true
		if writeErr := b.writeMeta(ctx, meta, now); writeErr != nil {
			return recordstore.Window{}, errors.Join(err, writeErr)
		}
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	ttl := meta.ExpiresAt.Sub(now)
	for _, chunk := range chunks {
		name := chunkName(chunk.first)
		if err := b.store.Set(ctx, b.key(stream, "chunk/"+name), chunk.payload, ttl); err != nil {
			return recordstore.Window{}, fmt.Errorf("stream %q: write chunk %s: %w", stream, name, err)
		}
		if err := b.store.ZAdd(ctx, b.key(stream, "index"), float64(chunk.first), name); err != nil {
			return recordstore.Window{}, fmt.Errorf("stream %q: index chunk %s: %w", stream, name, err)
		}
	}
	if len(chunks) > 0 {
		if err := b.store.Expire(ctx, b.key(stream, "index"), ttl); err != nil {
			return recordstore.Window{}, fmt.Errorf("stream %q: expire index: %w", stream, err)
		}
	}
	meta.Total += int64(len(rows))
	meta.HighSeq = window.To
	meta.UpdatedAt = now
	if err := b.writeMeta(ctx, meta, now); err != nil {
		return recordstore.Window{}, err
	}
	return window, nil
}

// openStream reads stream's metadata for an append, or starts it under kind.
func (b *Backend) openStream(ctx context.Context, stream, kind string, now time.Time) (recordstore.Meta, error) {
	meta, err := b.readMeta(ctx, stream)
	if errors.Is(err, recordstore.ErrNotFound) {
		expires := now.Add(b.ttl)
		meta := recordstore.NewStreamMeta(stream, kind, now)
		meta.ExpiresAt = &expires
		return meta, nil
	}
	if err != nil {
		return recordstore.Meta{}, err
	}
	if meta.Kind != kind {
		return recordstore.Meta{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, meta.Kind, kind)
	}
	return meta, nil
}

// Meta describes stream.
func (b *Backend) Meta(ctx context.Context, stream string) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	return b.readMeta(ctx, stream)
}

// pruneUncommitted removes every chunk an earlier, failed append left past
// the committed high seq. Left in place, an index entry for one would be read
// as rows the next append numbered differently.
func (b *Backend) pruneUncommitted(ctx context.Context, stream string, high int64) error {
	index := b.key(stream, "index")
	uncommitted := cache.Inclusive(float64(high + 1))
	names, err := b.store.ZRangeByScore(ctx, index, uncommitted, cache.PosInf)
	if err != nil {
		return fmt.Errorf("stream %q: list uncommitted chunks: %w", stream, err)
	}
	for _, name := range names {
		if err := b.store.Del(ctx, b.key(stream, "chunk/"+name)); err != nil {
			return fmt.Errorf("stream %q: remove uncommitted chunk %s: %w", stream, name, err)
		}
	}
	if len(names) == 0 {
		return nil
	}
	if err := b.store.ZRemRangeByScore(ctx, index, uncommitted, cache.PosInf); err != nil {
		return fmt.Errorf("stream %q: unindex uncommitted chunks: %w", stream, err)
	}
	return nil
}

// Scan reads stream's chunks in seq order, through the metadata's high seq.
// The chunks must cover every seq up to it: a store that lost the index or a
// chunk under memory pressure reads as an error, never as a shorter stream.
func (b *Backend) Scan(ctx context.Context, stream string, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	meta, err := b.Meta(ctx, stream)
	if err != nil {
		return err
	}
	firsts, err := b.chunkFirsts(ctx, stream)
	if err != nil {
		return err
	}
	next := int64(1)
	for index, first := range firsts {
		if first > meta.HighSeq {
			break
		}
		if first != next {
			return fmt.Errorf("stream %q: chunks jump from seq %d to %d below its high seq %d", stream, next-1, first, meta.HighSeq)
		}
		if index+1 < len(firsts) && firsts[index+1] <= afterSeq+1 {
			next = firsts[index+1]
			continue
		}
		if next, err = b.scanChunk(ctx, stream, first, afterSeq, meta.HighSeq, fn); err != nil {
			return err
		}
	}
	if next <= meta.HighSeq {
		return fmt.Errorf("stream %q holds rows through seq %d but its chunks reached seq %d; the store lost the rest",
			stream, meta.HighSeq, next-1)
	}
	return nil
}

// scanChunk calls fn with the chunk's rows after afterSeq and through high,
// and returns the seq after the last row it holds within high.
func (b *Backend) scanChunk(ctx context.Context, stream string, first, afterSeq, high int64, fn func(int64, recordstore.Row) error) (int64, error) {
	rows, err := b.readChunk(ctx, stream, first)
	if err != nil {
		return 0, err
	}
	next := first
	for _, row := range rows {
		if next > high {
			break
		}
		if next > afterSeq {
			if err := fn(next, row); err != nil {
				return 0, err
			}
		}
		next++
	}
	return next, nil
}

// Expire moves every key of stream to expire ttl from now.
func (b *Backend) Expire(ctx context.Context, stream string, ttl time.Duration) error {
	if err := recordstore.ValidateTTL(ttl); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	meta, err := b.Meta(ctx, stream)
	if err != nil {
		return err
	}
	firsts, err := b.chunkFirsts(ctx, stream)
	if err != nil {
		return err
	}
	for _, first := range firsts {
		if err := b.store.Expire(ctx, b.key(stream, "chunk/"+chunkName(first)), ttl); err != nil {
			return fmt.Errorf("stream %q: expire chunk %d: %w", stream, first, err)
		}
	}
	if err := b.store.Expire(ctx, b.key(stream, "index"), ttl); err != nil {
		return fmt.Errorf("stream %q: expire index: %w", stream, err)
	}
	now := b.now()
	expires := now.Add(ttl)
	meta.ExpiresAt = &expires
	return b.writeMeta(ctx, meta, now)
}

// Close releases nothing: the store belongs to the caller.
func (b *Backend) Close() error { return nil }

func (b *Backend) chunkFirsts(ctx context.Context, stream string) ([]int64, error) {
	names, err := b.store.ZRangeByScore(ctx, b.key(stream, "index"), cache.NegInf, cache.PosInf)
	if err != nil {
		return nil, fmt.Errorf("stream %q: list chunks: %w", stream, err)
	}
	firsts := make([]int64, len(names))
	for index, name := range names {
		first, err := strconv.ParseInt(name, 10, 64)
		if err != nil {
			return nil, fmt.Errorf("stream %q: chunk name %q is not a seq", stream, name)
		}
		firsts[index] = first
	}
	return firsts, nil
}
