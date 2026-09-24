// Package recordstore keeps append-only record streams: the rows a long
// capture produces, written as they arrive and read back by position instead
// of being carried inside whatever reported the capture.
//
// A stream is named by a stream id and holds rows of one kind. Every row gets
// a per-stream seq, contiguous from 1, which is the only position a reader
// resumes from — never a timestamp or a key, which neither order nor identify a
// row's position reliably. A kind may still declare a key, and then a stream
// holds each key once, which is what makes re-ingesting an overlapping source
// window idempotent. Rows leave a stream only from its low end: the whole
// stream expires, or Trim removes the oldest appends.
//
// Backends live in subpackages: kv (a clicky cache.Store: in-process memory or
// valkey/redis), sqlite (a file that is also the query index a profile reads),
// and ndjson (a file per stream). An Indexer mirrors any of them into a sqlite
// index incrementally, which is what makes a stream written by one process
// pageable through another's query engine.
//
// A stream has one writer at a time. Backends serialize appends made within
// one process; two processes appending to one stream is outside the contract.
package recordstore

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"time"

	"github.com/google/uuid"

	"github.com/flanksource/commons-db/query"
)

// Row is one record: a JSON-shaped map keyed by column name.
type Row = query.Row

var (
	// ErrNotFound reports a stream that does not exist, or no longer does.
	ErrNotFound = errors.New("record stream not found")

	// ErrCapacity reports an append refused because the stream, or one row of
	// it, would exceed what the backend was configured to hold. The rows of a
	// refused append are not written — none of them.
	ErrCapacity = errors.New("record stream capacity exceeded")

	// ErrSealed reports an append refused because its stream was sealed: the
	// writer declared it complete, and a reader has already taken it as such.
	ErrSealed = errors.New("record stream sealed")
)

// Window is an inclusive seq range. An empty window has From == To+1: an
// append of no rows reports the seq the next row will take.
type Window struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Len is the number of seqs the window spans.
func (w Window) Len() int64 { return w.To - w.From + 1 }

// AppendResult is what one append stored: the window its rows were numbered
// into, and how many rows it skipped because their key was already stored.
type AppendResult struct {
	Window  Window `json:"window"`
	Skipped int64  `json:"skipped"`
}

// Meta describes a stream.
type Meta struct {
	Stream     string `json:"stream"`
	Kind       string `json:"kind"`
	Generation string `json:"generation"`

	// Total is how many rows the stream holds, LowSeq the seq of the first one
	// and HighSeq the seq of the last. Rows only leave a stream from its low end
	// (Trim), so Total is HighSeq-LowSeq+1 in a durable backend; all three are
	// reported because an index mirroring it may lag. An empty stream has
	// LowSeq HighSeq+1.
	Total   int64 `json:"total"`
	LowSeq  int64 `json:"lowSeq"`
	HighSeq int64 `json:"highSeq"`

	UpdatedAt time.Time `json:"updatedAt"`

	// ExpiresAt is when the stream is removed, or nil when it is kept until
	// something expires it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`

	// Capped reports that an append was refused for capacity: the stream is
	// complete up to HighSeq and missing whatever that append carried.
	Capped bool `json:"capped,omitempty"`

	// Sealed reports that the writer declared the stream complete for now.
	// A reader that has read through HighSeq is done until an explicit Reopen;
	// readers revisiting a stream must fetch Meta again.
	Sealed bool `json:"sealed,omitempty"`
}

// NewStreamMeta starts one incarnation of stream. Generation distinguishes a
// stream id reused after expiry from the rows an index previously held for it.
func NewStreamMeta(stream, kind string, now time.Time) Meta {
	return Meta{Stream: stream, Kind: kind, Generation: uuid.NewString(), LowSeq: 1, UpdatedAt: now}
}

// Validate refuses metadata that cannot identify one stream incarnation.
func (m Meta) Validate() error {
	if err := ValidateStream(m.Stream); err != nil {
		return err
	}
	if err := ValidateKind(m.Kind); err != nil {
		return err
	}
	if m.Generation == "" {
		return fmt.Errorf("stream %q has no generation", m.Stream)
	}
	if m.LowSeq < 1 || m.LowSeq > m.HighSeq+1 {
		return fmt.Errorf("stream %q has low seq %d, which must be between 1 and its high seq %d plus one", m.Stream, m.LowSeq, m.HighSeq)
	}
	return nil
}

// Expired reports whether the stream's expiry has passed at now.
func (m Meta) Expired(now time.Time) bool {
	return m.ExpiresAt != nil && !now.Before(*m.ExpiresAt)
}

// Backend stores record streams.
type Backend interface {
	// Append adds rows to stream, creating it under kind when it does not
	// exist, and returns the window the rows were numbered into. Appending to
	// an existing stream under a different kind is an error.
	//
	// A keyed kind (KindOptions.Key) skips every row whose key the stream
	// already holds, atomically with the append, and numbers only the rows it
	// keeps; a batch naming one key twice is refused whole. A kind retaining
	// rows (RetainRows) slides the stream's expiry to the backend ttl from now
	// and trims the rows appended longer than that ago, in the same write.
	Append(ctx context.Context, stream, kind string, rows []Row) (AppendResult, error)

	// Meta describes stream, or returns ErrNotFound.
	Meta(ctx context.Context, stream string) (Meta, error)

	// Scan calls fn with every row after afterSeq, in seq order, stopping at
	// the first error fn returns. A scan from below the stream's low seq starts
	// at the low seq. An unknown stream is ErrNotFound.
	Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row Row) error) error

	// Trim removes the rows appended before before — every append up to the
	// last one made before it — and returns the stream's metadata after. The
	// rows kept keep their seqs, the low seq moves to the first of them, and
	// the keys of the rows removed may be appended again. An unknown stream is
	// ErrNotFound.
	Trim(ctx context.Context, stream string, before time.Time) (Meta, error)

	// Expire removes stream ttl from now, rows appended later included. The
	// ttl must be positive; an unknown stream is ErrNotFound.
	Expire(ctx context.Context, stream string, ttl time.Duration) error

	// Seal marks stream complete (Meta.Sealed): every later Append to it fails
	// with ErrSealed, while Scan, Trim and Expire still apply. Sealing a sealed
	// stream does nothing; an unknown stream is ErrNotFound. The seal ends with
	// the stream, so an id reused after expiry starts unsealed.
	Seal(ctx context.Context, stream string) error

	// Delete removes one stream and its rows immediately. An unknown stream is ErrNotFound.
	Delete(ctx context.Context, stream string) error

	Close() error
}

// Reopener permits a writer to resume a sealed stream after checking that its
// generation is still the one the caller recorded. Readers must take a fresh
// Meta after a resume; an earlier sealed snapshot remains final for its window.
type Reopener interface {
	Reopen(ctx context.Context, stream, generation string) error
}

var (
	streamIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,200}$`)
	kindPattern     = regexp.MustCompile(`^[A-Za-z][A-Za-z0-9_]{0,62}$`)
)

// ValidateStream rejects a stream id a backend could not key by. The alphabet
// is narrow on purpose: an id becomes a Redis key segment, a file name and a
// query parameter, and every character outside it is a character one of those
// would have to escape.
func ValidateStream(stream string) error {
	if !streamIDPattern.MatchString(stream) || stream == "." || stream == ".." {
		return fmt.Errorf("stream id %q must be 1-200 characters of A-Z a-z 0-9 . _ : -", stream)
	}
	return nil
}

// ValidateKind rejects a kind that could not name a table, a directory and a
// profile.
func ValidateKind(kind string) error {
	if !kindPattern.MatchString(kind) {
		return fmt.Errorf("kind %q must start with a letter and hold only letters, digits and _", kind)
	}
	return nil
}

// ValidateAppend checks an append's stream id and kind before a backend
// writes anything.
func ValidateAppend(stream, kind string) error {
	if err := ValidateStream(stream); err != nil {
		return err
	}
	return ValidateKind(kind)
}

// RefuseSealed is the error an append to meta's stream fails with once it is
// sealed, or nil while it is not.
func RefuseSealed(meta Meta) error {
	if !meta.Sealed {
		return nil
	}
	return fmt.Errorf("stream %q generation %q was sealed at seq %d: %w", meta.Stream, meta.Generation, meta.HighSeq, ErrSealed)
}

// ValidateTTL rejects an expiry that is not in the future.
func ValidateTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("expiry ttl must be positive, got %s", ttl)
	}
	return nil
}

// AppendTyped appends items as rows through their JSON encoding, which is the
// shape every backend stores and every reader sees.
func AppendTyped[T any](ctx context.Context, backend Backend, stream, kind string, items []T) (AppendResult, error) {
	rows := make([]Row, len(items))
	for index, item := range items {
		row, err := EncodeRow(item)
		if err != nil {
			return AppendResult{}, fmt.Errorf("stream %q row %d: %w", stream, index, err)
		}
		rows[index] = row
	}
	return backend.Append(ctx, stream, kind, rows)
}

// EncodeRow is item's JSON object as a row. Numbers are kept as json.Number so
// an int64 larger than a float64 can hold survives the trip.
func EncodeRow(item any) (Row, error) {
	encoded, err := json.Marshal(item)
	if err != nil {
		return nil, fmt.Errorf("encode row: %w", err)
	}
	return DecodeRow(encoded)
}

// DecodeRow reads one JSON object as a row, keeping numbers as json.Number.
func DecodeRow(encoded []byte) (Row, error) {
	decoder := json.NewDecoder(bytes.NewReader(encoded))
	decoder.UseNumber()
	var row Row
	if err := decoder.Decode(&row); err != nil {
		return nil, fmt.Errorf("decode row: %w", err)
	}
	if row == nil {
		return nil, fmt.Errorf("decode row: %s is not a JSON object", encoded)
	}
	return row, nil
}
