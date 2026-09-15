package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/recordstore"
)

// chunkLayout stores an unkeyed kind's rows in chunks, under <prefix>/<stream>/:
//
//	index            a sorted set of chunk names, scored by the chunk's first seq
//	chunk/<first>    one append's rows as a JSON array, first zero-padded
//	appended         a sorted set of chunk names, scored by their append time
//
// Each append writes whole new chunks and never rewrites one, so a stream of
// ten thousand rows costs ten thousand row encodings rather than a growing blob
// rewritten per append. Every key carries the stream's expiry, so the stream
// leaves the store in one piece.
type chunkLayout struct{ b *Backend }

// chunk is one stored JSON array of rows and the seq its first row takes.
type chunk struct {
	first   int64
	payload []byte
}

// splitChunks packs rows, numbered from first, into JSON arrays no larger than
// maxBytes. A row that cannot fit a chunk on its own refuses the whole append.
func splitChunks(rows []recordstore.Row, first int64, maxBytes int) ([]chunk, error) {
	var (
		chunks  []chunk
		current bytes.Buffer
		start   = first
	)
	flush := func(next int64) {
		if current.Len() == 0 {
			return
		}
		current.WriteByte(']')
		chunks = append(chunks, chunk{first: start, payload: bytes.Clone(current.Bytes())})
		current.Reset()
		start = next
	}
	for index, row := range rows {
		seq := first + int64(index)
		encoded, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("encode row %d: %w", seq, err)
		}
		if len(encoded)+2 > maxBytes {
			return nil, fmt.Errorf("row %d encodes to %d bytes, over the %d-byte chunk cap: %w",
				seq, len(encoded), maxBytes, recordstore.ErrCapacity)
		}
		if current.Len() > 0 && current.Len()+1+len(encoded)+1 > maxBytes {
			flush(seq)
		}
		if current.Len() == 0 {
			current.WriteByte('[')
		} else {
			current.WriteByte(',')
		}
		current.Write(encoded)
	}
	flush(first + int64(len(rows)))
	return chunks, nil
}

// prune removes every chunk an earlier, failed append left past the committed
// high seq. Left in place, an index entry for one would be read as rows the
// next append numbered differently.
func (l chunkLayout) prune(ctx context.Context, meta recordstore.Meta, _ bool) error {
	b, stream := l.b, meta.Stream
	index := b.key(stream, "index")
	uncommitted := cache.Inclusive(float64(meta.HighSeq + 1))
	names, err := b.store.ZRangeByScore(ctx, index, uncommitted, cache.PosInf)
	if err != nil {
		return fmt.Errorf("stream %q: list uncommitted chunks: %w", stream, err)
	}
	if len(names) == 0 {
		return nil
	}
	if err := l.removeChunks(ctx, stream, names); err != nil {
		return err
	}
	if err := b.store.ZRemRangeByScore(ctx, index, uncommitted, cache.PosInf); err != nil {
		return fmt.Errorf("stream %q: unindex uncommitted chunks: %w", stream, err)
	}
	return nil
}

// unstored keeps every row: an unkeyed kind stores each row appended.
func (chunkLayout) unstored(_ context.Context, _ recordstore.Meta, rows []recordstore.Row, _ []string) ([]recordstore.Row, []string, int64, error) {
	return rows, nil, 0, nil
}

// write stores rows as chunks with their index and append time entries. A row
// too large for a chunk marks the stream capped and refuses the append.
func (l chunkLayout) write(ctx context.Context, meta *recordstore.Meta, rows []recordstore.Row, _ []string, window recordstore.Window, now time.Time, _ time.Duration) error {
	b, stream := l.b, meta.Stream
	chunks, err := splitChunks(rows, window.From, b.maxChunkBytes)
	if err != nil {
		return b.refuseOversized(ctx, meta, now, err)
	}
	if len(chunks) == 0 {
		return nil
	}
	ttl := meta.ExpiresAt.Sub(now)
	for _, chunk := range chunks {
		name := paddedSeq(chunk.first)
		if err := b.store.Set(ctx, b.key(stream, "chunk/"+name), chunk.payload, ttl); err != nil {
			return fmt.Errorf("stream %q: write chunk %s: %w", stream, name, err)
		}
		if err := b.store.ZAdd(ctx, b.key(stream, "index"), float64(chunk.first), name); err != nil {
			return fmt.Errorf("stream %q: index chunk %s: %w", stream, name, err)
		}
		if err := b.store.ZAdd(ctx, b.key(stream, "appended"), appendScore(now), name); err != nil {
			return fmt.Errorf("stream %q: record chunk %s append time: %w", stream, name, err)
		}
	}
	return l.expireSets(ctx, stream, ttl)
}

// slide moves every chunk to the stream's new expiry: a chunk is written to
// expire with the stream, so each one moves when the stream does.
func (l chunkLayout) slide(ctx context.Context, meta recordstore.Meta, now time.Time) error {
	return l.expire(ctx, meta, meta.ExpiresAt.Sub(now), 0)
}

// expire moves every chunk and set of the stream to expire ttl from now.
func (l chunkLayout) expire(ctx context.Context, meta recordstore.Meta, ttl, _ time.Duration) error {
	b, stream := l.b, meta.Stream
	firsts, err := l.chunkFirsts(ctx, stream)
	if err != nil {
		return err
	}
	for _, first := range firsts {
		if err := b.store.Expire(ctx, b.key(stream, "chunk/"+paddedSeq(first)), ttl); err != nil {
			return fmt.Errorf("stream %q: expire chunk %d: %w", stream, first, err)
		}
	}
	return l.expireSets(ctx, stream, ttl)
}

func (l chunkLayout) expireSets(ctx context.Context, stream string, ttl time.Duration) error {
	for _, set := range []string{"index", "appended"} {
		if err := l.b.store.Expire(ctx, l.b.key(stream, set), ttl); err != nil {
			return fmt.Errorf("stream %q: expire %s: %w", stream, set, err)
		}
	}
	return nil
}

// scan reads the stream's chunks in seq order. The chunks must cover every seq
// between its low and high seq.
func (l chunkLayout) scan(ctx context.Context, meta recordstore.Meta, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	stream := meta.Stream
	firsts, err := l.chunkFirsts(ctx, stream)
	if err != nil {
		return err
	}
	next := meta.LowSeq
	for index, first := range firsts {
		if first > meta.HighSeq {
			break
		}
		// A chunk below the low seq is one a trim committed away but had not
		// deleted yet.
		if first < meta.LowSeq {
			continue
		}
		if first != next {
			return fmt.Errorf("stream %q: chunks jump from seq %d to %d below its high seq %d", stream, next-1, first, meta.HighSeq)
		}
		if index+1 < len(firsts) && firsts[index+1] <= afterSeq+1 {
			next = firsts[index+1]
			continue
		}
		if next, err = l.scanChunk(ctx, stream, first, afterSeq, meta.HighSeq, fn); err != nil {
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
func (l chunkLayout) scanChunk(ctx context.Context, stream string, first, afterSeq, high int64, fn func(int64, recordstore.Row) error) (int64, error) {
	rows, err := l.readChunk(ctx, stream, first)
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

func (l chunkLayout) readChunk(ctx context.Context, stream string, first int64) ([]recordstore.Row, error) {
	payload, err := l.b.store.Get(ctx, l.b.key(stream, "chunk/"+paddedSeq(first)))
	if errors.Is(err, cache.ErrKeyNotFound) {
		return nil, fmt.Errorf("stream %q: chunk %d is missing though its index names it (expired?)", stream, first)
	}
	if err != nil {
		return nil, fmt.Errorf("stream %q: read chunk %d: %w", stream, first, err)
	}
	var raw []json.RawMessage
	if err := json.Unmarshal(payload, &raw); err != nil {
		return nil, fmt.Errorf("stream %q: decode chunk %d: %w", stream, first, err)
	}
	rows := make([]recordstore.Row, len(raw))
	for index, encoded := range raw {
		if rows[index], err = recordstore.DecodeRow(encoded); err != nil {
			return nil, fmt.Errorf("stream %q: chunk %d row %d: %w", stream, first, index, err)
		}
	}
	return rows, nil
}

// trim moves the stream's low seq past every chunk appended before before,
// commits that in the metadata, and only then deletes what it moved past: a
// crash in between leaves chunks below the low seq, which no reader looks at
// and the next trim deletes.
func (l chunkLayout) trim(ctx context.Context, meta recordstore.Meta, before, now time.Time) (recordstore.Meta, error) {
	b, stream := l.b, meta.Stream
	appended, err := b.store.ZRangeByScore(ctx, b.key(stream, "appended"), cache.NegInf, cache.Exclusive(appendScore(before)))
	if err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: list chunks appended before %s: %w", stream, before, err)
	}
	if len(appended) == 0 {
		return meta, nil
	}
	firsts, err := l.chunkFirsts(ctx, stream)
	if err != nil {
		return recordstore.Meta{}, err
	}
	if cut := trimCut(meta, firsts, appended); cut > meta.LowSeq {
		meta.LowSeq, meta.Total = cut, meta.HighSeq-cut+1
		if err := b.writeMeta(ctx, meta, now); err != nil {
			return recordstore.Meta{}, err
		}
	}
	return meta, l.removeBelow(ctx, stream, firsts, meta.LowSeq)
}

// trimCut is the first seq kept once every retained chunk up to the last one
// named in appended is trimmed: the first seq of the chunk after it, or the
// seq after the high seq. Nothing named keeps the low seq.
func trimCut(meta recordstore.Meta, firsts []int64, appended []string) int64 {
	named := make(map[string]bool, len(appended))
	for _, name := range appended {
		named[name] = true
	}
	cut := meta.LowSeq
	for index, first := range firsts {
		if first > meta.HighSeq {
			break
		}
		if first < meta.LowSeq || !named[paddedSeq(first)] {
			continue
		}
		cut = meta.HighSeq + 1
		if index+1 < len(firsts) && firsts[index+1] <= meta.HighSeq {
			cut = firsts[index+1]
		}
	}
	return cut
}

// removeBelow deletes every chunk and index entry below lowSeq.
func (l chunkLayout) removeBelow(ctx context.Context, stream string, firsts []int64, lowSeq int64) error {
	var names []string
	for _, first := range firsts {
		if first < lowSeq {
			names = append(names, paddedSeq(first))
		}
	}
	if err := l.removeChunks(ctx, stream, names); err != nil {
		return err
	}
	if err := l.b.store.ZRemRangeByScore(ctx, l.b.key(stream, "index"), cache.NegInf, cache.Exclusive(float64(lowSeq))); err != nil {
		return fmt.Errorf("stream %q: unindex chunks below seq %d: %w", stream, lowSeq, err)
	}
	return nil
}

// removeChunks deletes the named chunks and their append times.
func (l chunkLayout) removeChunks(ctx context.Context, stream string, names []string) error {
	for _, name := range names {
		if err := l.b.store.Del(ctx, l.b.key(stream, "chunk/"+name)); err != nil {
			return fmt.Errorf("stream %q: remove chunk %s: %w", stream, name, err)
		}
		if err := l.b.store.ZRem(ctx, l.b.key(stream, "appended"), name); err != nil {
			return fmt.Errorf("stream %q: remove chunk %s append time: %w", stream, name, err)
		}
	}
	return nil
}

func (l chunkLayout) chunkFirsts(ctx context.Context, stream string) ([]int64, error) {
	names, err := l.b.store.ZRangeByScore(ctx, l.b.key(stream, "index"), cache.NegInf, cache.PosInf)
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
