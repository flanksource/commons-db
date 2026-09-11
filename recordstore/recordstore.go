// Package recordstore keeps append-only record streams: the rows a long
// capture produces, written as they arrive and read back by position instead
// of being carried inside whatever reported the capture.
//
// A stream is named by a stream id and holds rows of one kind. Every row gets
// a per-stream seq, contiguous from 1, which is the only position a reader
// resumes from — never a timestamp or a key, which neither order nor identify a
// row reliably.
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
)

// Window is an inclusive seq range. An empty window has From == To+1: an
// append of no rows reports the seq the next row will take.
type Window struct {
	From int64 `json:"from"`
	To   int64 `json:"to"`
}

// Len is the number of seqs the window spans.
func (w Window) Len() int64 { return w.To - w.From + 1 }

// Meta describes a stream.
type Meta struct {
	Stream     string `json:"stream"`
	Kind       string `json:"kind"`
	Generation string `json:"generation"`

	// Total is how many rows the stream holds and HighSeq the seq of the last
	// one. A stream only ever appends, so they are equal in a durable backend;
	// they are both reported because an index mirroring it may lag.
	Total   int64 `json:"total"`
	HighSeq int64 `json:"highSeq"`

	UpdatedAt time.Time `json:"updatedAt"`

	// ExpiresAt is when the stream is removed, or nil when it is kept until
	// something expires it.
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`

	// Capped reports that an append was refused for capacity: the stream is
	// complete up to HighSeq and missing whatever that append carried.
	Capped bool `json:"capped,omitempty"`
}

// NewStreamMeta starts one incarnation of stream. Generation distinguishes a
// stream id reused after expiry from the rows an index previously held for it.
func NewStreamMeta(stream, kind string, now time.Time) Meta {
	return Meta{Stream: stream, Kind: kind, Generation: uuid.NewString(), UpdatedAt: now}
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
	Append(ctx context.Context, stream, kind string, rows []Row) (Window, error)

	// Meta describes stream, or returns ErrNotFound.
	Meta(ctx context.Context, stream string) (Meta, error)

	// Scan calls fn with every row after afterSeq, in seq order, stopping at
	// the first error fn returns. An unknown stream is ErrNotFound.
	Scan(ctx context.Context, stream string, afterSeq int64, fn func(seq int64, row Row) error) error

	// Expire removes stream ttl from now, rows appended later included. The
	// ttl must be positive; an unknown stream is ErrNotFound.
	Expire(ctx context.Context, stream string, ttl time.Duration) error

	Close() error
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

// ValidateTTL rejects an expiry that is not in the future.
func ValidateTTL(ttl time.Duration) error {
	if ttl <= 0 {
		return fmt.Errorf("expiry ttl must be positive, got %s", ttl)
	}
	return nil
}

// AppendTyped appends items as rows through their JSON encoding, which is the
// shape every backend stores and every reader sees.
func AppendTyped[T any](ctx context.Context, backend Backend, stream, kind string, items []T) (Window, error) {
	rows := make([]Row, len(items))
	for index, item := range items {
		row, err := EncodeRow(item)
		if err != nil {
			return Window{}, fmt.Errorf("stream %q row %d: %w", stream, index, err)
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
