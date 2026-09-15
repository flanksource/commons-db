package kv

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/recordstore"
)

// rowLayout stores a keyed kind's rows one key each, under <prefix>/<stream>/:
//
//	row/<key>   the row, with the generation and seq it was committed under
//	seqs        "<seq>/<key>" members scored by seq: the order a scan reads in
//	appended    the same members scored by append time: what a trim removes
//
// Whether a key is already stored is one read of its row, and an append writes
// only its own rows, so an append costs its batch whatever the stream holds.
//
// No append moves a stored row's expiry. A row is written to expire retention
// after the stream's expiry at its append instead, which outlives every moment
// the stream can still report it: a stream is readable until the ttl after its
// latest append, and that append trimmed every row appended more than the ttl
// before it. Only the metadata and the two sets move with each append. A row
// trimmed away is left to that expiry: its seq is below the low seq, so no scan
// or key lookup reads it.
type rowLayout struct{ b *Backend }

// storedRow is a row as a row key holds it. The generation and seq tie it to
// one committed position, so a row an interrupted append or an expired
// incarnation of the stream left behind is never taken for a stored one.
type storedRow struct {
	Generation string          `json:"generation"`
	Seq        int64           `json:"seq"`
	Row        json.RawMessage `json:"row"`
}

func rowMember(seq int64, key string) string { return paddedSeq(seq) + "/" + key }

func parseRowMember(stream, member string) (int64, string, error) {
	if len(member) <= seqWidth || member[seqWidth] != '/' {
		return 0, "", fmt.Errorf("stream %q: row member %q is not <seq>/<key>", stream, member)
	}
	seq, err := strconv.ParseInt(member[:seqWidth], 10, 64)
	if err != nil {
		return 0, "", fmt.Errorf("stream %q: row member %q has no seq: %w", stream, member, err)
	}
	return seq, member[seqWidth+1:], nil
}

func parseRowMembers(stream string, members []string) ([]int64, []string, error) {
	seqs, keys := make([]int64, len(members)), make([]string, len(members))
	for index, member := range members {
		seq, key, err := parseRowMember(stream, member)
		if err != nil {
			return nil, nil, err
		}
		seqs[index], keys[index] = seq, key
	}
	return seqs, keys, nil
}

func (l rowLayout) rowKey(stream, key string) string { return l.b.key(stream, "row/"+key) }

// readRows reads the row of every key in one MGet, and calls fn with each in
// key order: its position in keys, the row, and false when the store holds no
// row at the key. It stops at fn's first error and returns it.
func (l rowLayout) readRows(ctx context.Context, stream string, keys []string, fn func(index int, stored storedRow, found bool) error) error {
	rowKeys := func(yield func(string) bool) {
		for _, key := range keys {
			if !yield(l.rowKey(stream, key)) {
				return
			}
		}
	}
	index := 0
	for entry, err := range l.b.store.MGet(ctx, rowKeys) {
		if err != nil {
			return fmt.Errorf("stream %q: read rows: %w", stream, err)
		}
		if index >= len(keys) || entry.Key != l.rowKey(stream, keys[index]) {
			return fmt.Errorf("stream %q: the store answered key %q for row %d of %d", stream, entry.Key, index, len(keys))
		}
		var stored storedRow
		if entry.Found {
			if err := json.Unmarshal(entry.Value, &stored); err != nil {
				return fmt.Errorf("stream %q: decode row %q: %w", stream, keys[index], err)
			}
		}
		if err := fn(index, stored, entry.Found); err != nil {
			return err
		}
		index++
	}
	if index != len(keys) {
		return fmt.Errorf("stream %q: the store answered %d of %d rows", stream, index, len(keys))
	}
	return nil
}

// committed reports whether stored is a row meta's stream holds.
func committed(meta recordstore.Meta, stored storedRow) bool {
	return stored.Generation == meta.Generation && stored.Seq >= meta.LowSeq && stored.Seq <= meta.HighSeq
}

// prune removes the rows and set entries an interrupted append left past the
// committed high seq, so no row it wrote reads as committed once the next
// append numbers its own rows over the same seqs. A stream just created drops
// the sets an expired incarnation left instead: that incarnation's rows carry
// its generation, which no lookup takes for this one's.
func (l rowLayout) prune(ctx context.Context, meta recordstore.Meta, created bool) error {
	b, stream := l.b, meta.Stream
	if created {
		for _, set := range []string{"seqs", "appended"} {
			if err := b.store.Del(ctx, b.key(stream, set)); err != nil {
				return fmt.Errorf("stream %q: drop the %s of an expired incarnation: %w", stream, set, err)
			}
		}
		return nil
	}
	uncommitted := cache.Inclusive(float64(meta.HighSeq + 1))
	members, err := b.store.ZRangeByScore(ctx, b.key(stream, "seqs"), uncommitted, cache.PosInf)
	if err != nil {
		return fmt.Errorf("stream %q: list uncommitted rows: %w", stream, err)
	}
	if len(members) == 0 {
		return nil
	}
	seqs, keys, err := parseRowMembers(stream, members)
	if err != nil {
		return err
	}
	var uncommittedRows []string
	err = l.readRows(ctx, stream, keys, func(index int, stored storedRow, found bool) error {
		if found && stored.Generation == meta.Generation && stored.Seq == seqs[index] {
			uncommittedRows = append(uncommittedRows, keys[index])
		}
		return nil
	})
	if err != nil {
		return err
	}
	for _, key := range uncommittedRows {
		if err := b.store.Del(ctx, l.rowKey(stream, key)); err != nil {
			return fmt.Errorf("stream %q: remove uncommitted row %q: %w", stream, key, err)
		}
	}
	for _, member := range members {
		if err := b.store.ZRem(ctx, b.key(stream, "appended"), member); err != nil {
			return fmt.Errorf("stream %q: remove uncommitted append time %q: %w", stream, member, err)
		}
	}
	if err := b.store.ZRemRangeByScore(ctx, b.key(stream, "seqs"), uncommitted, cache.PosInf); err != nil {
		return fmt.Errorf("stream %q: remove uncommitted seqs: %w", stream, err)
	}
	return nil
}

// trim moves the low seq past every row appended before before, commits that,
// and then drops the set entries below it. A crash in between leaves entries
// below the low seq, which nothing reads and the next trim drops.
func (l rowLayout) trim(ctx context.Context, meta recordstore.Meta, before, now time.Time) (recordstore.Meta, error) {
	b, stream := l.b, meta.Stream
	cutoff := cache.Exclusive(appendScore(before))
	members, err := b.store.ZRangeByScore(ctx, b.key(stream, "appended"), cache.NegInf, cutoff)
	if err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: list rows appended before %s: %w", stream, before, err)
	}
	if len(members) == 0 {
		return meta, nil
	}
	cut := meta.LowSeq
	for _, member := range members {
		seq, _, err := parseRowMember(stream, member)
		if err != nil {
			return recordstore.Meta{}, err
		}
		if seq >= cut && seq <= meta.HighSeq {
			cut = seq + 1
		}
	}
	if cut > meta.LowSeq {
		meta.LowSeq, meta.Total = cut, meta.HighSeq-cut+1
		if err := b.writeMeta(ctx, meta, now); err != nil {
			return recordstore.Meta{}, err
		}
	}
	if err := b.store.ZRemRangeByScore(ctx, b.key(stream, "appended"), cache.NegInf, cutoff); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: drop append times before %s: %w", stream, before, err)
	}
	if err := b.store.ZRemRangeByScore(ctx, b.key(stream, "seqs"), cache.NegInf, cache.Exclusive(float64(meta.LowSeq))); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: drop seqs below %d: %w", stream, meta.LowSeq, err)
	}
	return meta, nil
}

// unstored reads the row of each key in the append, and keeps the rows whose
// key the stream does not hold committed.
func (l rowLayout) unstored(ctx context.Context, meta recordstore.Meta, rows []recordstore.Row, keys []string) ([]recordstore.Row, []string, int64, error) {
	stored := make(map[string]bool, len(keys))
	if meta.Total > 0 {
		err := l.readRows(ctx, meta.Stream, keys, func(index int, row storedRow, found bool) error {
			stored[keys[index]] = found && committed(meta, row)
			return nil
		})
		if err != nil {
			return nil, nil, 0, err
		}
	}
	kept, keptKeys, skipped := recordstore.Unstored(rows, keys, func(key string) bool { return stored[key] })
	return kept, keptKeys, skipped, nil
}

// write adds the rows' set entries first and the rows after them, so a crash
// part way leaves nothing prune cannot find. A row too large for the cap marks
// the stream capped and refuses the whole append before anything is written.
func (l rowLayout) write(ctx context.Context, meta *recordstore.Meta, rows []recordstore.Row, keys []string, window recordstore.Window, now time.Time, retention time.Duration) error {
	b, stream := l.b, meta.Stream
	payloads := make([][]byte, len(rows))
	for index, row := range rows {
		seq := window.From + int64(index)
		encoded, err := json.Marshal(row)
		if err != nil {
			return fmt.Errorf("stream %q: encode row %d: %w", stream, seq, err)
		}
		if payloads[index], err = json.Marshal(storedRow{Generation: meta.Generation, Seq: seq, Row: encoded}); err != nil {
			return fmt.Errorf("stream %q: encode row %d: %w", stream, seq, err)
		}
		if len(payloads[index]) > b.maxChunkBytes {
			return b.refuseOversized(ctx, meta, now, fmt.Errorf("row %d encodes to %d bytes, over the %d-byte row cap: %w",
				seq, len(payloads[index]), b.maxChunkBytes, recordstore.ErrCapacity))
		}
	}
	if len(rows) == 0 {
		return nil
	}
	for index, key := range keys {
		seq := window.From + int64(index)
		member := rowMember(seq, key)
		if err := b.store.ZAdd(ctx, b.key(stream, "seqs"), float64(seq), member); err != nil {
			return fmt.Errorf("stream %q: record seq %d of row %q: %w", stream, seq, key, err)
		}
		if err := b.store.ZAdd(ctx, b.key(stream, "appended"), appendScore(now), member); err != nil {
			return fmt.Errorf("stream %q: record append time of row %q: %w", stream, key, err)
		}
	}
	ttl := meta.ExpiresAt.Sub(now)
	for index, key := range keys {
		if err := b.store.Set(ctx, l.rowKey(stream, key), payloads[index], ttl+retention); err != nil {
			return fmt.Errorf("stream %q: write row %q: %w", stream, key, err)
		}
	}
	// A retaining stream's slide moves the sets on every append.
	if retention > 0 {
		return nil
	}
	return l.expireSets(ctx, stream, ttl)
}

// slide moves the sets to the stream's new expiry. The rows stay put.
func (l rowLayout) slide(ctx context.Context, meta recordstore.Meta, now time.Time) error {
	return l.expireSets(ctx, meta.Stream, meta.ExpiresAt.Sub(now))
}

// expire moves each committed row to expire retention after the stream's new
// expiry, and the sets with the stream.
func (l rowLayout) expire(ctx context.Context, meta recordstore.Meta, ttl, retention time.Duration) error {
	b, stream := l.b, meta.Stream
	members, err := b.store.ZRangeByScore(ctx, b.key(stream, "seqs"), cache.Inclusive(float64(meta.LowSeq)), cache.Inclusive(float64(meta.HighSeq)))
	if err != nil {
		return fmt.Errorf("stream %q: list rows: %w", stream, err)
	}
	for _, member := range members {
		_, key, err := parseRowMember(stream, member)
		if err != nil {
			return err
		}
		if err := b.store.Expire(ctx, l.rowKey(stream, key), ttl+retention); err != nil {
			return fmt.Errorf("stream %q: expire row %q: %w", stream, key, err)
		}
	}
	return l.expireSets(ctx, stream, ttl)
}

func (l rowLayout) expireSets(ctx context.Context, stream string, ttl time.Duration) error {
	for _, set := range []string{"seqs", "appended"} {
		if err := l.b.store.Expire(ctx, l.b.key(stream, set), ttl); err != nil {
			return fmt.Errorf("stream %q: expire %s: %w", stream, set, err)
		}
	}
	return nil
}

// scan reads the rows after afterSeq through the high seq in seq order. Every
// seq must name a row committed under it: one the store lost reads as an error,
// never as a shorter stream.
func (l rowLayout) scan(ctx context.Context, meta recordstore.Meta, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	stream := meta.Stream
	first := max(afterSeq+1, meta.LowSeq)
	if first > meta.HighSeq {
		return nil
	}
	members, err := l.b.store.ZRangeByScore(ctx, l.b.key(stream, "seqs"), cache.Inclusive(float64(first)), cache.Inclusive(float64(meta.HighSeq)))
	if err != nil {
		return fmt.Errorf("stream %q: list rows after seq %d: %w", stream, afterSeq, err)
	}
	seqs, keys, err := parseRowMembers(stream, members)
	if err != nil {
		return err
	}
	for index, seq := range seqs {
		if expected := first + int64(index); seq != expected {
			return fmt.Errorf("stream %q: seqs jump from %d to %d below its high seq %d", stream, expected-1, seq, meta.HighSeq)
		}
	}
	if reached := first + int64(len(seqs)) - 1; reached < meta.HighSeq {
		return fmt.Errorf("stream %q holds rows through seq %d but its seqs reached seq %d; the store lost the rest",
			stream, meta.HighSeq, reached)
	}
	return l.readRows(ctx, stream, keys, func(index int, stored storedRow, found bool) error {
		seq, key := seqs[index], keys[index]
		if !found {
			return fmt.Errorf("stream %q: row at seq %d, key %q, is missing though the stream holds it (evicted?)", stream, seq, key)
		}
		if stored.Generation != meta.Generation || stored.Seq != seq {
			return fmt.Errorf("stream %q: row key %q holds seq %d of generation %q, not seq %d of %q",
				stream, key, stored.Seq, stored.Generation, seq, meta.Generation)
		}
		row, err := recordstore.DecodeRow(stored.Row)
		if err != nil {
			return fmt.Errorf("stream %q: row at seq %d: %w", stream, seq, err)
		}
		return fn(seq, row)
	})
}
