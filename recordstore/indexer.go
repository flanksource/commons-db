package recordstore

import (
	"context"
	"fmt"
	"time"
)

// indexBatch is how many rows one import into the index carries.
const indexBatch = 500

// Index is a backend that takes rows under the seqs another backend gave them:
// what an Indexer mirrors a source into. The sqlite backend is one.
type Index interface {
	Backend

	// Prepare reconciles the storage for source's kind and returns the indexed
	// incarnation of its stream. A derived index removes an older generation
	// under the same stream id and reports it absent.
	Prepare(ctx context.Context, source Meta) (indexed Meta, found bool, err error)

	// Import stores request.Rows under their source seqs. First must be the seq
	// after the indexed stream's high seq, or for a stream the index does not
	// hold yet the source's low seq; a gap or different generation fails.
	Import(ctx context.Context, request ImportRequest) (Window, error)

	// TrimBelow mirrors a source trim: it drops the indexed rows below lowSeq,
	// and moves an indexed stream that had not reached lowSeq up to it empty.
	TrimBelow(ctx context.Context, stream string, lowSeq int64) (Meta, error)

	// Derived reports whether the index can discard rows and rebuild them from
	// a separate source.
	Derived() bool

	// SetExpiry mirrors the source's absolute expiry. Nil keeps the indexed
	// stream while its source exists.
	SetExpiry(ctx context.Context, stream string, expiresAt *time.Time) error
}

// ImportRequest is one source window copied into an index.
type ImportRequest struct {
	Source Meta
	First  int64
	Rows   []Row
}

// Indexer keeps an Index caught up with a source backend, one stream at a
// time and incrementally by seq: each Ensure copies only what the source
// gained since the last one.
type Indexer struct {
	source Backend
	index  Index
	// same reports that the source is the index, which leaves nothing to copy.
	same  bool
	locks StreamLocks
	now   func() time.Time
}

// NewIndexer mirrors source into index.
func NewIndexer(source Backend, index Index) (*Indexer, error) {
	if source == nil || index == nil {
		return nil, fmt.Errorf("an indexer needs both a source and an index")
	}
	same := Backend(index) == source
	if same && index.Derived() {
		return nil, fmt.Errorf("an index that is its own source cannot be derived: rebuilding it would discard the authoritative rows")
	}
	if !same && !index.Derived() {
		return nil, fmt.Errorf("an index separate from its source must be derived so it can be rebuilt")
	}
	return &Indexer{source: source, index: index, same: same, now: time.Now}, nil
}

// Ensure brings stream's index up to the source's high seq. A stream the
// source does not have is ErrNotFound. When the source is the index there is
// nothing to copy, and Ensure only confirms the stream exists.
func (i *Indexer) Ensure(ctx context.Context, stream string) error {
	unlock := i.locks.Lock(stream)
	defer unlock()
	source, err := i.source.Meta(ctx, stream)
	if err != nil {
		return fmt.Errorf("index stream %q: %w", stream, err)
	}
	if err := source.Validate(); err != nil {
		return fmt.Errorf("index stream %q: source metadata: %w", stream, err)
	}
	indexed, found, err := i.prepare(ctx, source)
	if err != nil {
		return err
	}
	if i.same {
		if !found {
			return fmt.Errorf("index stream %q: its source index lost the stream while preparing it: %w", stream, ErrNotFound)
		}
		return nil
	}
	if indexed, err = i.catchUp(ctx, source, indexed, found); err != nil {
		return err
	}
	latest, err := i.source.Meta(ctx, stream)
	if err != nil {
		return fmt.Errorf("index stream %q: re-read source metadata: %w", stream, err)
	}
	if latest.Generation != source.Generation {
		return fmt.Errorf("index stream %q changed generation from %q to %q while it was being indexed", stream, source.Generation, latest.Generation)
	}
	if indexed, err = i.mirrorTrim(ctx, latest, indexed); err != nil {
		return err
	}
	if indexed, err = i.mirrorReopen(ctx, latest, indexed); err != nil {
		return err
	}
	if err := i.mirrorExpiry(ctx, latest, indexed); err != nil {
		return err
	}
	return i.mirrorSeal(ctx, latest, indexed)
}

// prepare asks the index to prepare source's stream, checking the metadata it
// returns describes that stream.
func (i *Indexer) prepare(ctx context.Context, source Meta) (Meta, bool, error) {
	stream := source.Stream
	indexed, found, err := i.index.Prepare(ctx, source)
	switch {
	case err != nil:
		return Meta{}, false, fmt.Errorf("index stream %q: prepare: %w", stream, err)
	case !found:
		return indexed, false, nil
	}
	if err := indexed.Validate(); err != nil {
		return Meta{}, false, fmt.Errorf("index stream %q: prepared metadata: %w", stream, err)
	}
	if indexed.Stream != source.Stream || indexed.Kind != source.Kind {
		return Meta{}, false, fmt.Errorf("index stream %q: prepare returned stream %q kind %q for source kind %q",
			stream, indexed.Stream, indexed.Kind, source.Kind)
	}
	return indexed, true, nil
}

// catchUp brings a separate index of source's stream to its high seq: trimmed
// as the source is, then filled with the rows it lacks.
func (i *Indexer) catchUp(ctx context.Context, source, indexed Meta, found bool) (Meta, error) {
	stream := source.Stream
	if found && indexed.Generation != source.Generation {
		return Meta{}, fmt.Errorf("index stream %q: prepare returned generation %q for source generation %q", stream, indexed.Generation, source.Generation)
	}
	if indexed.HighSeq > source.HighSeq {
		return Meta{}, fmt.Errorf("index of stream %q generation %q is ahead of its source (seq %d, source %d)",
			stream, source.Generation, indexed.HighSeq, source.HighSeq)
	}
	var err error
	if found {
		if indexed, err = i.mirrorReopen(ctx, source, indexed); err != nil {
			return Meta{}, err
		}
		if indexed, err = i.mirrorTrim(ctx, source, indexed); err != nil {
			return Meta{}, err
		}
	}
	if !found || indexed.HighSeq < source.HighSeq {
		// A stream the index does not hold yet starts at the source's low seq:
		// the rows below it are trimmed and no scan returns them.
		high := max(indexed.HighSeq, source.LowSeq-1)
		if indexed.HighSeq, err = i.copyAfter(ctx, source, high, !found); err != nil {
			return Meta{}, err
		}
		indexed.LowSeq = max(indexed.LowSeq, source.LowSeq)
	}
	return indexed, nil
}

// mirrorReopen reopens a sealed index whose source has resumed, so readers do
// not take the stale seal as the end of the stream.
func (i *Indexer) mirrorReopen(ctx context.Context, source, indexed Meta) (Meta, error) {
	if !indexed.Sealed || source.Sealed {
		return indexed, nil
	}
	reopener, ok := i.index.(Reopener)
	if !ok {
		return Meta{}, fmt.Errorf("index of stream %q cannot reopen after its source resumed", source.Stream)
	}
	if err := reopener.Reopen(ctx, source.Stream, source.Generation); err != nil {
		return Meta{}, fmt.Errorf("index stream %q: reopen: %w", source.Stream, err)
	}
	indexed.Sealed = false
	return indexed, nil
}

// mirrorSeal seals the index once it holds every row of a sealed source. An
// index still short of the source's high seq stays open, so the next Ensure
// can import the rest and seal it then.
func (i *Indexer) mirrorSeal(ctx context.Context, source, indexed Meta) error {
	if !source.Sealed || indexed.Sealed || indexed.HighSeq < source.HighSeq {
		return nil
	}
	if err := i.index.Seal(ctx, source.Stream); err != nil {
		return fmt.Errorf("index stream %q: seal: %w", source.Stream, err)
	}
	return nil
}

// mirrorTrim drops the indexed rows the source no longer holds.
func (i *Indexer) mirrorTrim(ctx context.Context, source, indexed Meta) (Meta, error) {
	if indexed.LowSeq >= source.LowSeq {
		return indexed, nil
	}
	trimmed, err := i.index.TrimBelow(ctx, source.Stream, source.LowSeq)
	if err != nil {
		return Meta{}, fmt.Errorf("index stream %q: trim below seq %d: %w", source.Stream, source.LowSeq, err)
	}
	if trimmed.LowSeq != source.LowSeq {
		return Meta{}, fmt.Errorf("index stream %q: trim below seq %d left low seq %d", source.Stream, source.LowSeq, trimmed.LowSeq)
	}
	return trimmed, nil
}

// copyAfter imports every source row after high, checking the seqs arrive
// without a gap and reach the high seq the source reported. A source stream
// with no rows past high is imported as an empty stream when the index does
// not hold it yet (create). It returns the last seq the index holds after.
//
// A scan that ends early is refused rather than taken as the stream: an index
// that stopped short would page the stream as complete, and every later Ensure
// would find it already caught up.
func (i *Indexer) copyAfter(ctx context.Context, source Meta, high int64, create bool) (int64, error) {
	stream := source.Stream
	next := high + 1
	var batch []Row
	flush := func() error {
		expected := Window{From: next, To: next + int64(len(batch)) - 1}
		window, err := i.index.Import(ctx, ImportRequest{Source: source, First: next, Rows: batch})
		if err != nil {
			return fmt.Errorf("index stream %q: %w", stream, err)
		}
		if window != expected {
			return fmt.Errorf("index stream %q: import returned window %+v, expected %+v", stream, window, expected)
		}
		next, batch = window.To+1, nil
		return nil
	}
	err := i.source.Scan(ctx, stream, high, func(seq int64, row Row) error {
		if expected := next + int64(len(batch)); seq != expected {
			return fmt.Errorf("source stream %q skips from seq %d to %d", stream, expected-1, seq)
		}
		batch = append(batch, row)
		if len(batch) < indexBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return 0, fmt.Errorf("index stream %q: %w", stream, err)
	}
	if reached := next + int64(len(batch)) - 1; reached < source.HighSeq {
		return 0, fmt.Errorf("index stream %q: the source holds rows through seq %d but its scan reached seq %d", stream, source.HighSeq, reached)
	}
	if len(batch) > 0 || create {
		if err := flush(); err != nil {
			return 0, err
		}
	}
	return next - 1, nil
}

// expiryTolerance avoids rewriting an expiry for harmless timestamp precision
// differences in an index implementation.
const expiryTolerance = time.Second

// mirrorExpiry gives the index the source's expiry, so an index entry never
// outlives the stream it describes.
func (i *Indexer) mirrorExpiry(ctx context.Context, source, indexed Meta) error {
	if source.ExpiresAt == nil && indexed.ExpiresAt == nil {
		return nil
	}
	if source.ExpiresAt != nil && indexed.ExpiresAt != nil && indexed.ExpiresAt.Sub(*source.ExpiresAt).Abs() < expiryTolerance {
		return nil
	}
	if source.ExpiresAt != nil && !source.ExpiresAt.After(i.now()) {
		return fmt.Errorf("index stream %q: source expired at %s: %w", source.Stream, source.ExpiresAt, ErrNotFound)
	}
	if err := i.index.SetExpiry(ctx, source.Stream, source.ExpiresAt); err != nil {
		return fmt.Errorf("index stream %q: %w", source.Stream, err)
	}
	return nil
}
