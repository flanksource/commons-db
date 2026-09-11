package kv

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/clicky/cache"

	"github.com/flanksource/commons-db/recordstore"
)

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

func (b *Backend) readChunk(ctx context.Context, stream string, first int64) ([]recordstore.Row, error) {
	payload, err := b.store.Get(ctx, b.key(stream, "chunk/"+chunkName(first)))
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
	// A ttl the store reads as "persist" would outlive every chunk it names.
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
