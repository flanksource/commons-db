package recordresults

import (
	"context"
	"errors"
	"fmt"
	"os"
	"time"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	"github.com/flanksource/commons-db/recordstore/kv"
	"github.com/flanksource/commons-db/recordstore/ndjson"
	"github.com/flanksource/commons-db/recordstore/sqlite"
)

// StreamRef points at a window of a record stream as the store holding it
// described the stream: what a capture reports instead of carrying its rows,
// and what a reader replays them from after the capturing process is gone.
type StreamRef struct {
	Stream string `json:"stream"`
	Kind   string `json:"kind"`

	// Generation is the incarnation of the stream the seqs belong to.
	Generation string `json:"generation"`

	// Low and High are the committed seq bounds when the ref was taken.
	Low  int64 `json:"low"`
	High int64 `json:"high"`

	// From and To are the inclusive seq window the ref names: a step's
	// checkpoint, or Low..High.
	From int64 `json:"from"`
	To   int64 `json:"to"`

	Total     int64      `json:"total"`
	ExpiresAt *time.Time `json:"expiresAt,omitempty"`

	Store StoreLocation `json:"store"`
}

// EventsRef is the ref as a session status records it. Its JSON is the ref's
// own, field for field.
func (r StreamRef) EventsRef() *query.EventsRef {
	return &query.EventsRef{
		Stream: r.Stream, Kind: r.Kind, Generation: r.Generation, Low: r.Low, High: r.High,
		From: r.From, To: r.To, Total: r.Total, ExpiresAt: r.ExpiresAt,
		Store: query.EventsStoreLocation{Backend: string(r.Store.Backend), Host: r.Store.Host, File: r.Store.File},
	}
}

// StoreLocation is where a stream's rows are: the backend kind, and for a
// local file the host and file holding them. A kv stream has neither; it is
// read back through the store the environment names.
type StoreLocation struct {
	Backend recordstore.BackendKind `json:"backend"`
	Host    string                  `json:"host,omitempty"`
	File    string                  `json:"file,omitempty"`
}

// Ref describes the window from..to of stream from the backend's own
// metadata and the store the backend reports holding it. A from or to of 0
// means the stream's low or high seq. A window reaching outside the rows the
// stream holds is refused rather than clamped: a ref promises its rows exist.
func (r *Results) Ref(ctx context.Context, stream string, from, to int64) (StreamRef, error) {
	if r.Backend == nil {
		return StreamRef{}, errors.New("stream ref: the result store is not open")
	}
	meta, err := r.Backend.Meta(ctx, stream)
	if err != nil {
		return StreamRef{}, fmt.Errorf("stream ref %q: %w", stream, err)
	}
	window, err := refWindow(meta, from, to)
	if err != nil {
		return StreamRef{}, fmt.Errorf("stream ref %q: %w", stream, err)
	}
	location, err := r.location(ctx, stream)
	if err != nil {
		return StreamRef{}, fmt.Errorf("stream ref %q: %w", stream, err)
	}
	return StreamRef{
		Stream: meta.Stream, Kind: meta.Kind, Generation: meta.Generation, Low: meta.LowSeq, High: meta.HighSeq,
		From: window.From, To: window.To, Total: meta.Total, ExpiresAt: meta.ExpiresAt, Store: location,
	}, nil
}

// refWindow resolves a requested window against meta's bounds. An empty
// window (From == To+1) is allowed, which is what an empty stream's is.
func refWindow(meta recordstore.Meta, from, to int64) (recordstore.Window, error) {
	if from < 0 || to < 0 {
		return recordstore.Window{}, fmt.Errorf("window %d..%d holds a negative seq", from, to)
	}
	window := recordstore.Window{From: from, To: to}
	if window.From == 0 {
		window.From = meta.LowSeq
	}
	if window.To == 0 {
		window.To = meta.HighSeq
	}
	switch {
	case window.From > window.To+1:
		return recordstore.Window{}, fmt.Errorf("window %d..%d starts after it ends", window.From, window.To)
	case window.From < meta.LowSeq:
		return recordstore.Window{}, fmt.Errorf("window %d..%d starts below its low seq %d, whose rows are trimmed", window.From, window.To, meta.LowSeq)
	case window.To > meta.HighSeq:
		return recordstore.Window{}, fmt.Errorf("window %d..%d reaches past its high seq %d", window.From, window.To, meta.HighSeq)
	}
	return window, nil
}

// location is where the backend holding stream for ctx keeps it, as that
// backend reports it: a Router is resolved to its route's backend first.
func (r *Results) location(ctx context.Context, stream string) (StoreLocation, error) {
	backend := r.Backend.Unwrap()
	if router, routed := backend.(*recordstore.Router); routed {
		resolved, err := router.Resolve(ctx)
		if err != nil {
			return StoreLocation{}, err
		}
		backend = resolved
	}
	switch typed := backend.(type) {
	case *kv.Backend:
		return StoreLocation{Backend: recordstore.BackendKV}, nil
	case *sqlite.Backend:
		return localLocation(recordstore.BackendSQLite, typed.Path())
	case *ndjson.Backend:
		file, err := typed.File(ctx, stream)
		if err != nil {
			return StoreLocation{}, err
		}
		return localLocation(recordstore.BackendNDJSON, file)
	default:
		return StoreLocation{}, fmt.Errorf("a %T backend reports no location", backend)
	}
}

// localLocation is file on this host.
func localLocation(backend recordstore.BackendKind, file string) (StoreLocation, error) {
	host, err := os.Hostname()
	if err != nil {
		return StoreLocation{}, fmt.Errorf("name the host holding %s: %w", file, err)
	}
	return StoreLocation{Backend: backend, Host: host, File: file}, nil
}
