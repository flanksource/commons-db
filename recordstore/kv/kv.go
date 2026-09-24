// Package kv stores record streams in a clicky cache.Store, so one
// implementation runs in process (cache.NewMemory) and against valkey/redis
// (clicky/valkey.NewStore) — the backend a CLI writes to and a server reads.
//
// A stream is keys under <prefix>/<stream>/, laid out by its kind. An unkeyed
// kind's rows arrive in bulk and are stored in chunks (chunks.go); a keyed
// kind's rows are stored one key each (rows.go), so what an append asks about a
// row is one read of that row, whatever else the stream holds. Both layouts
// share:
//
//	meta             the stream's recordstore.Meta as JSON
//
// The metadata is the commit point: its low and high seq bound everything a
// reader or a key lookup believes, so an entry an interrupted write left
// outside them is invisible, and the next write removes it.
package kv

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/recordstore"
)

// seqWidth zero-pads a seq in a key or member, so a listing sorts in seq order
// too, not only a score.
const seqWidth = 19

// Options configure a kv backend.
type Options struct {
	// Store holds the keys. It is owned by the caller and not closed.
	Store cache.Store

	// Prefix namespaces every key the backend writes.
	Prefix string

	// Schema resolves a kind to its key and retention. A kind it refuses
	// cannot be written or read.
	Schema recordstore.SchemaResolver

	// TTL is how long a stream lives from its first append unless Expire moves
	// it, or how long a row of a kind retaining rows lives from its own append.
	// It is required: the store is memory, and a stream nobody expires is a
	// leak.
	TTL time.Duration

	// MaxChunkBytes caps one stored chunk, or one stored keyed row. An unkeyed
	// append is split into as many chunks as it needs; a single row larger than
	// the cap is refused with recordstore.ErrCapacity.
	MaxChunkBytes int

	// Now is the clock stream metadata is stamped with. Nil is time.Now.
	Now func() time.Time
}

// Backend is a recordstore.Backend over a cache.Store.
type Backend struct {
	store         cache.Store
	prefix        string
	schema        recordstore.SchemaResolver
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
	case options.Schema == nil:
		return nil, fmt.Errorf("kv record store: a schema resolver is required")
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
		store: options.Store, prefix: strings.TrimRight(options.Prefix, "/"), schema: options.Schema, ttl: options.TTL,
		maxChunkBytes: options.MaxChunkBytes, now: now,
	}, nil
}

func (b *Backend) key(stream, suffix string) string { return b.prefix + "/" + stream + "/" + suffix }

func paddedSeq(seq int64) string { return fmt.Sprintf("%0*d", seqWidth, seq) }

// appendScore is an append time as a sorted-set score: microseconds, which a
// float64 holds exactly for every instant this code will see.
func appendScore(at time.Time) float64 { return float64(at.UnixMicro()) }

// layout is how a kind's streams are laid out in the store. Every method but
// scan runs under the stream's lock.
type layout interface {
	// prune removes what an interrupted append left past meta's high seq, or,
	// for a stream just created, what an expired incarnation left behind.
	prune(ctx context.Context, meta recordstore.Meta, created bool) error

	// trim moves the low seq past every row appended before before, commits
	// it, and removes what it moved past.
	trim(ctx context.Context, meta recordstore.Meta, before, now time.Time) (recordstore.Meta, error)

	// unstored keeps the rows whose keys meta's committed rows do not hold.
	unstored(ctx context.Context, meta recordstore.Meta, rows []recordstore.Row, keys []string) ([]recordstore.Row, []string, int64, error)

	// write stores rows, numbered into window, ahead of the metadata that
	// commits them. retention is how long a row outlives the stream's expiry.
	write(ctx context.Context, meta *recordstore.Meta, rows []recordstore.Row, keys []string, window recordstore.Window, now time.Time, retention time.Duration) error

	// slide moves what a row-retaining append keeps alive to meta's expiry.
	slide(ctx context.Context, meta recordstore.Meta, now time.Time) error

	// scan calls fn with meta's rows after afterSeq, in seq order.
	scan(ctx context.Context, meta recordstore.Meta, afterSeq int64, fn func(int64, recordstore.Row) error) error

	// expire moves every key of meta's stream to expire ttl from now, and its
	// rows retention after that.
	expire(ctx context.Context, meta recordstore.Meta, ttl, retention time.Duration) error
}

func (b *Backend) layoutOf(schema recordstore.KindSchema) layout {
	if schema.Options.Key != "" {
		return rowLayout{b}
	}
	return chunkLayout{b}
}

// layoutFor resolves the layout and retention of an existing stream's kind.
func (b *Backend) layoutFor(meta recordstore.Meta) (layout, time.Duration, error) {
	schema, err := recordstore.ResolveKind(b.schema, meta.Kind)
	if err != nil {
		return nil, 0, fmt.Errorf("stream %q: %w", meta.Stream, err)
	}
	retention, err := schema.RetentionTTL(b.ttl)
	if err != nil {
		return nil, 0, fmt.Errorf("stream %q: %w", meta.Stream, err)
	}
	return b.layoutOf(schema), retention, nil
}

// Append writes the rows it keeps ahead of the metadata that makes them
// visible. A reader bounds itself by the metadata's high seq, so a crash part
// way leaves entries past it that no reader sees; the next append removes them
// before numbering its own rows over the same seqs.
func (b *Backend) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return recordstore.AppendResult{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	now := b.now()
	meta, created, err := b.openStream(ctx, stream, kind, now)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	schema, retention, keys, err := b.resolveAppend(stream, kind, rows)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	layout := b.layoutOf(schema)
	if err := layout.prune(ctx, meta, created); err != nil {
		return recordstore.AppendResult{}, err
	}
	if retention > 0 {
		if meta, err = layout.trim(ctx, meta, now.Add(-retention), now); err != nil {
			return recordstore.AppendResult{}, err
		}
		expires := now.Add(retention)
		meta.ExpiresAt = &expires
	}
	kept, keptKeys, skipped, err := layout.unstored(ctx, meta, rows, keys)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	window := recordstore.Window{From: meta.HighSeq + 1, To: meta.HighSeq + int64(len(kept))}
	if err := layout.write(ctx, &meta, kept, keptKeys, window, now, retention); err != nil {
		return recordstore.AppendResult{}, err
	}
	if schema.Options.Retention == recordstore.RetainRows {
		if err := layout.slide(ctx, meta, now); err != nil {
			return recordstore.AppendResult{}, err
		}
	}
	meta.Total += int64(len(kept))
	meta.HighSeq = window.To
	meta.UpdatedAt = now
	if err := b.writeMeta(ctx, meta, now); err != nil {
		return recordstore.AppendResult{}, err
	}
	return recordstore.AppendResult{Window: window, Skipped: skipped}, nil
}

// resolveAppend checks an append against its kind before anything is written:
// the kind's schema, the retention the append applies and the rows' keys.
func (b *Backend) resolveAppend(stream, kind string, rows []recordstore.Row) (recordstore.KindSchema, time.Duration, []string, error) {
	schema, err := recordstore.ResolveKind(b.schema, kind)
	if err != nil {
		return recordstore.KindSchema{}, 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	retention, err := schema.RetentionTTL(b.ttl)
	if err != nil {
		return recordstore.KindSchema{}, 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	keys, err := schema.RowKeys(rows)
	if err != nil {
		return recordstore.KindSchema{}, 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	return schema, retention, keys, nil
}

// openStream reads stream's metadata for an append, or starts it under kind
// and reports it created.
func (b *Backend) openStream(ctx context.Context, stream, kind string, now time.Time) (recordstore.Meta, bool, error) {
	meta, err := b.readMeta(ctx, stream)
	if errors.Is(err, recordstore.ErrNotFound) {
		expires := now.Add(b.ttl)
		meta := recordstore.NewStreamMeta(stream, kind, now)
		meta.ExpiresAt = &expires
		return meta, true, nil
	}
	if err != nil {
		return recordstore.Meta{}, false, err
	}
	if meta.Kind != kind {
		return recordstore.Meta{}, false, fmt.Errorf("stream %q holds kind %q, not %q", stream, meta.Kind, kind)
	}
	return meta, false, recordstore.RefuseSealed(meta)
}

// Seal marks stream complete in its metadata, the commit point every append
// reads under the stream's lock.
func (b *Backend) Seal(ctx context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	meta, err := b.readMeta(ctx, stream)
	if err != nil || meta.Sealed {
		return err
	}
	now := b.now()
	meta.Sealed, meta.UpdatedAt = true, now
	return b.writeMeta(ctx, meta, now)
}

func (b *Backend) Reopen(ctx context.Context, stream, generation string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	meta, err := b.readMeta(ctx, stream)
	if err != nil {
		return err
	}
	if !meta.Sealed || meta.Generation != generation || generation == "" {
		return fmt.Errorf("stream %q: sealed generation %q was not found", stream, generation)
	}
	now := b.now()
	meta.Sealed, meta.UpdatedAt = false, now
	return b.writeMeta(ctx, meta, now)
}

// Meta describes stream.
func (b *Backend) Meta(ctx context.Context, stream string) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	return b.readMeta(ctx, stream)
}

// Scan reads stream's rows in seq order, from its low seq through its high
// seq. The store must hold every seq between them: one that lost a row, a
// chunk or an index under memory pressure reads as an error, never as a shorter
// stream.
func (b *Backend) Scan(ctx context.Context, stream string, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	meta, err := b.Meta(ctx, stream)
	if err != nil {
		return err
	}
	layout, _, err := b.layoutFor(meta)
	if err != nil {
		return err
	}
	return layout.scan(ctx, meta, afterSeq, fn)
}

// Trim removes the rows appended before before.
func (b *Backend) Trim(ctx context.Context, stream string, before time.Time) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	meta, err := b.readMeta(ctx, stream)
	if err != nil {
		return recordstore.Meta{}, err
	}
	layout, _, err := b.layoutFor(meta)
	if err != nil {
		return recordstore.Meta{}, err
	}
	return layout.trim(ctx, meta, before, b.now())
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
	layout, retention, err := b.layoutFor(meta)
	if err != nil {
		return err
	}
	if err := layout.expire(ctx, meta, ttl, retention); err != nil {
		return err
	}
	now := b.now()
	expires := now.Add(ttl)
	meta.ExpiresAt = &expires
	return b.writeMeta(ctx, meta, now)
}

// Close releases nothing: the store belongs to the caller.
func (b *Backend) Close() error { return nil }

// readMeta reads the stored metadata. A stream past its expiry reads as not
// found even before the store reaps it, so every backend agrees on the moment
// a stream ends.
func (b *Backend) readMeta(ctx context.Context, stream string) (recordstore.Meta, error) {
	payload, err := b.store.Get(ctx, b.key(stream, "meta"))
	if errors.Is(err, cache.ErrKeyNotFound) {
		return recordstore.Meta{}, fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound)
	}
	if err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: read meta: %w", stream, err)
	}
	var meta recordstore.Meta
	if err := json.Unmarshal(payload, &meta); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: decode meta: %w", stream, err)
	}
	if err := meta.Validate(); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: invalid meta: %w", stream, err)
	}
	if meta.Expired(b.now()) {
		return recordstore.Meta{}, fmt.Errorf("stream %q expired at %s: %w", stream, meta.ExpiresAt, recordstore.ErrNotFound)
	}
	return meta, nil
}

func (b *Backend) writeMeta(ctx context.Context, meta recordstore.Meta, now time.Time) error {
	// A ttl the store reads as "persist" would outlive every key it names.
	ttl := meta.ExpiresAt.Sub(now)
	if ttl <= 0 {
		return fmt.Errorf("stream %q expired at %s while being written: %w", meta.Stream, meta.ExpiresAt, recordstore.ErrNotFound)
	}
	payload, err := json.Marshal(meta)
	if err != nil {
		return fmt.Errorf("stream %q: encode meta: %w", meta.Stream, err)
	}
	if err := b.store.Set(ctx, b.key(meta.Stream, "meta"), payload, ttl); err != nil {
		return fmt.Errorf("stream %q: write meta: %w", meta.Stream, err)
	}
	return nil
}

// refuseOversized marks the stream capped and refuses an append carrying a row
// the backend's cap cannot hold.
func (b *Backend) refuseOversized(ctx context.Context, meta *recordstore.Meta, now time.Time, err error) error {
	meta.Capped = true
	if writeErr := b.writeMeta(ctx, *meta, now); writeErr != nil {
		return errors.Join(err, writeErr)
	}
	return fmt.Errorf("stream %q: %w", meta.Stream, err)
}
