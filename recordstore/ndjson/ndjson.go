// Package ndjson stores each record stream as a local file: one line per row
// in <dir>/<kind>/<stream>.ndjson, written {"seq":N,"row":{…}}, beside a
// <stream>.meta.json sidecar holding the stream's recordstore.Meta. A trimmed
// stream's rows move to <stream>@<low seq>.ndjson, which the sidecar names.
//
// It is the durable backend a CLI run writes when no shared store is
// configured. Two bounds keep it from filling a disk nobody watches: a per
// stream byte cap, which refuses an append loudly rather than dropping it, and
// a per kind count of streams kept, which removes the oldest when a new one
// opens.
package ndjson

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/recordstore"
)

const (
	dataSuffix = ".ndjson"
	metaSuffix = ".meta.json"

	// trimmedSeparator joins a stream id to the low seq of its trimmed data
	// file. It is outside the stream id alphabet (recordstore.ValidateStream).
	trimmedSeparator = "@"
)

// Options configure an ndjson backend.
type Options struct {
	// Dir holds a directory per kind.
	Dir string

	// Schema resolves a kind to its key and retention. A kind it refuses
	// cannot be written.
	Schema recordstore.SchemaResolver

	// MaxBytes caps one stream's file. An append that would take it past the
	// cap is refused whole with recordstore.ErrCapacity and the stream is
	// marked capped.
	MaxBytes int64

	// KeepStreams is how many streams of one kind the directory keeps. Opening
	// a new stream removes the least recently written beyond it; a stream
	// being written at that moment is left alone.
	KeepStreams int

	// TTL is how long a stream lives from its first append unless Expire moves
	// it, or how long a row of a kind retaining rows lives from its own append.
	// Zero keeps a stream until it is expired or rotated out, and refuses a
	// kind retaining rows.
	TTL time.Duration

	// Now is the clock streams are stamped and expired by. Nil is time.Now.
	Now func() time.Time
}

// Backend is a recordstore.Backend over a directory of files.
type Backend struct {
	dir      string
	schema   recordstore.SchemaResolver
	maxBytes int64
	keep     int
	ttl      time.Duration
	now      func() time.Time
	locks    recordstore.StreamLocks

	// opening serializes creating a stream with the rotation it triggers, so
	// two new streams never both count themselves under the budget.
	opening sync.Mutex
}

var _ recordstore.Backend = (*Backend)(nil)

// sidecar is what the .meta.json file holds: the stream's metadata, the data
// file its rows are in and the length of it the last committed write left.
// Anything past that length is a torn append, and is cut before the next one.
type sidecar struct {
	recordstore.Meta
	File  string `json:"file"`
	Bytes int64  `json:"bytes"`

	// Appends is every committed append still holding rows, oldest first: what
	// Trim finds the rows appended before an instant through.
	Appends []appendMark `json:"appends,omitempty"`

	// Keys maps each key a keyed stream holds to the seq of its row.
	Keys map[string]int64 `json:"keys,omitempty"`
}

// appendMark is one append: the seq of the last row it stored, and when.
type appendMark struct {
	Last int64     `json:"last"`
	At   time.Time `json:"at"`
}

type line struct {
	Seq int64           `json:"seq"`
	Row json.RawMessage `json:"row"`
}

// New validates options and returns the backend.
func New(options Options) (*Backend, error) {
	switch {
	case strings.TrimSpace(options.Dir) == "":
		return nil, fmt.Errorf("ndjson record store: a directory is required")
	case options.Schema == nil:
		return nil, fmt.Errorf("ndjson record store: a schema resolver is required")
	case options.MaxBytes <= 0:
		return nil, fmt.Errorf("ndjson record store: a positive per-stream byte cap is required")
	case options.KeepStreams <= 0:
		return nil, fmt.Errorf("ndjson record store: a positive count of streams to keep is required")
	case options.TTL < 0:
		return nil, fmt.Errorf("ndjson record store: stream ttl cannot be negative")
	}
	dir, err := filepath.Abs(options.Dir)
	if err != nil {
		return nil, fmt.Errorf("ndjson record store: resolve %s: %w", options.Dir, err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	return &Backend{
		dir: dir, schema: options.Schema, maxBytes: options.MaxBytes, keep: options.KeepStreams, ttl: options.TTL, now: now,
	}, nil
}

// metaPath is stream's sidecar under kind.
func (b *Backend) metaPath(kind, stream string) string {
	return filepath.Join(b.dir, kind, stream+metaSuffix)
}

// dataPath is the data file state names.
func (b *Backend) dataPath(state sidecar) string {
	return filepath.Join(b.dir, state.Kind, state.File)
}

// Append encodes the rows it keeps as lines after the stream's high seq and
// appends them.
func (b *Backend) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return recordstore.AppendResult{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	if existing, err := b.find(stream); err == nil && existing.Kind != kind {
		return recordstore.AppendResult{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, existing.Kind, kind)
	}
	retention, keys, err := b.resolveAppend(stream, kind, rows)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	state, err := b.openStream(stream, kind)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	now := b.now()
	if retention > 0 {
		if state, err = b.trimLocked(state, now.Add(-retention)); err != nil {
			return recordstore.AppendResult{}, err
		}
		expires := now.Add(retention)
		state.ExpiresAt = &expires
	}
	kept, keptKeys, skipped := recordstore.Unstored(rows, keys, func(key string) bool {
		_, stored := state.Keys[key]
		return stored
	})
	window := recordstore.Window{From: state.HighSeq + 1, To: state.HighSeq + int64(len(kept))}
	if err := b.appendRows(&state, kept, keptKeys, window, now); err != nil {
		return recordstore.AppendResult{}, err
	}
	if err := b.writeSidecar(state); err != nil {
		return recordstore.AppendResult{}, err
	}
	return recordstore.AppendResult{Window: window, Skipped: skipped}, nil
}

// resolveAppend checks an append against its kind before anything is written:
// the retention it applies and the rows' keys.
func (b *Backend) resolveAppend(stream, kind string, rows []recordstore.Row) (time.Duration, []string, error) {
	schema, err := recordstore.ResolveKind(b.schema, kind)
	if err != nil {
		return 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	retention, err := schema.RetentionTTL(b.ttl)
	if err != nil {
		return 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	keys, err := schema.RowKeys(rows)
	if err != nil {
		return 0, nil, fmt.Errorf("stream %q: %w", stream, err)
	}
	return retention, keys, nil
}

// appendRows writes rows, numbered into window, to the data file and records
// them in state for the caller to commit. An append past the byte cap marks
// the stream capped and is refused.
func (b *Backend) appendRows(state *sidecar, rows []recordstore.Row, keys []string, window recordstore.Window, now time.Time) error {
	encoded, err := encodeLines(rows, window.From)
	if err != nil {
		return fmt.Errorf("stream %q: %w", state.Stream, err)
	}
	if size := state.Bytes + int64(len(encoded)); size > b.maxBytes {
		state.Capped, state.UpdatedAt = true, now
		if writeErr := b.writeSidecar(*state); writeErr != nil {
			return errors.Join(writeErr, recordstore.ErrCapacity)
		}
		return fmt.Errorf("stream %q: %d rows would take its file to %d bytes, over the %d-byte cap: %w",
			state.Stream, len(rows), size, b.maxBytes, recordstore.ErrCapacity)
	}
	if err := b.appendData(*state, encoded); err != nil {
		return err
	}
	state.Bytes += int64(len(encoded))
	state.Total += int64(len(rows))
	state.HighSeq, state.UpdatedAt = window.To, now
	if len(rows) > 0 {
		state.Appends = append(state.Appends, appendMark{Last: window.To, At: now})
	}
	for index, key := range keys {
		if state.Keys == nil {
			state.Keys = make(map[string]int64, len(keys))
		}
		state.Keys[key] = window.From + int64(index)
	}
	return nil
}

// openStream reads stream for an append, or starts it under kind — rotating
// the kind's oldest streams out first.
func (b *Backend) openStream(stream, kind string) (sidecar, error) {
	state, err := b.find(stream)
	if err == nil {
		if state.Kind != kind {
			return sidecar{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, state.Kind, kind)
		}
		return state, recordstore.RefuseSealed(state.Meta)
	}
	if !errors.Is(err, recordstore.ErrNotFound) {
		return sidecar{}, err
	}
	b.opening.Lock()
	defer b.opening.Unlock()
	if err := os.MkdirAll(filepath.Join(b.dir, kind), 0o750); err != nil {
		return sidecar{}, fmt.Errorf("stream %q: create %s: %w", stream, kind, err)
	}
	if err := b.rotate(kind, stream); err != nil {
		return sidecar{}, err
	}
	now := b.now()
	state = sidecar{Meta: recordstore.NewStreamMeta(stream, kind, now), File: stream + dataSuffix}
	if b.ttl > 0 {
		expires := now.Add(b.ttl)
		state.ExpiresAt = &expires
	}
	if err := b.writeSidecar(state); err != nil {
		return sidecar{}, err
	}
	return state, nil
}

func encodeLines(rows []recordstore.Row, first int64) ([]byte, error) {
	var encoded []byte
	for index, row := range rows {
		payload, err := json.Marshal(row)
		if err != nil {
			return nil, fmt.Errorf("encode row %d: %w", first+int64(index), err)
		}
		entry, err := json.Marshal(line{Seq: first + int64(index), Row: payload})
		if err != nil {
			return nil, fmt.Errorf("encode row %d: %w", first+int64(index), err)
		}
		encoded = append(append(encoded, entry...), '\n')
	}
	return encoded, nil
}

// appendData cuts the file back to its committed length, then appends.
func (b *Backend) appendData(state sidecar, encoded []byte) error {
	path := b.dataPath(state)
	file, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("stream %q: open %s: %w", state.Stream, path, err)
	}
	defer func() { _ = file.Close() }()
	if err := file.Truncate(state.Bytes); err != nil {
		return fmt.Errorf("stream %q: trim %s to its committed length: %w", state.Stream, path, err)
	}
	if _, err := file.WriteAt(encoded, state.Bytes); err != nil {
		return fmt.Errorf("stream %q: write %s: %w", state.Stream, path, err)
	}
	return nil
}

// Meta describes stream.
func (b *Backend) Meta(_ context.Context, stream string) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	state, err := b.find(stream)
	return state.Meta, err
}

// Scan reads stream's file in seq order up to its committed high seq, starting
// at the first line after afterSeq rather than at the top of the file.
func (b *Backend) Scan(_ context.Context, stream string, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	state, err := b.find(stream)
	if err != nil {
		return err
	}
	first := max(afterSeq+1, state.LowSeq)
	if first > state.HighSeq {
		return nil
	}
	path := b.dataPath(state)
	file, err := os.Open(path)
	if err != nil {
		return fmt.Errorf("stream %q: open %s: %w", stream, path, err)
	}
	defer func() { _ = file.Close() }()
	offset, err := lineAfter(file, state.Bytes, first-1)
	if err != nil {
		return fmt.Errorf("stream %q: find seq %d in %s: %w", stream, first, path, err)
	}
	section := io.NewSectionReader(file, offset, state.Bytes-offset)
	return scanLines(bufio.NewReader(section), path, first, state.HighSeq, fn)
}

// scanLines reads lines first..high, checking each holds the seq its position
// says it should.
func scanLines(reader *bufio.Reader, path string, first, high int64, fn func(int64, recordstore.Row) error) error {
	for expected := first; expected <= high; expected++ {
		raw, err := reader.ReadBytes('\n')
		if err != nil {
			return fmt.Errorf("%s: seq %d: %w", path, expected, err)
		}
		var entry line
		if err := json.Unmarshal(raw, &entry); err != nil {
			return fmt.Errorf("%s: seq %d: %w", path, expected, err)
		}
		if entry.Seq != expected {
			return fmt.Errorf("%s: line %d holds seq %d", path, expected, entry.Seq)
		}
		row, err := recordstore.DecodeRow(entry.Row)
		if err != nil {
			return fmt.Errorf("%s: seq %d: %w", path, entry.Seq, err)
		}
		if err := fn(entry.Seq, row); err != nil {
			return err
		}
	}
	return nil
}

// Expire moves stream's expiry to ttl from now.
func (b *Backend) Expire(_ context.Context, stream string, ttl time.Duration) error {
	if err := recordstore.ValidateTTL(ttl); err != nil {
		return err
	}
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil {
		return err
	}
	expires := b.now().Add(ttl)
	state.ExpiresAt = &expires
	return b.writeSidecar(state)
}

// File is the data file stream's committed rows are in. A trim moves them to a
// new one, so it names the file as of the call.
func (b *Backend) File(_ context.Context, stream string) (string, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return "", err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil {
		return "", err
	}
	return b.dataPath(state), nil
}

// Seal marks stream complete in its sidecar.
func (b *Backend) Seal(_ context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil || state.Sealed {
		return err
	}
	state.Sealed, state.UpdatedAt = true, b.now()
	return b.writeSidecar(state)
}

func (b *Backend) Reopen(_ context.Context, stream, generation string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	state, err := b.find(stream)
	if err != nil {
		return err
	}
	if !state.Sealed || state.Generation != generation || generation == "" {
		return fmt.Errorf("stream %q: sealed generation %q was not found", stream, generation)
	}
	state.Sealed, state.UpdatedAt = false, b.now()
	return b.writeSidecar(state)
}

// Close releases nothing: every call opens and closes its own files.
func (b *Backend) Close() error { return nil }
