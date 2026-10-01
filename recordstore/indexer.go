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

	// DeleteSeqs mirrors a source compaction: it drops the indexed rows at
	// seqs and records the source's compaction count.
	DeleteSeqs(ctx context.Context, stream string, seqs []int64, compactions int64) (Meta, error)
}

// ImportRequest is one source window copied into an index.
type ImportRequest struct {
	Source Meta
	First  int64
	Rows   []Row

	// Seqs is the source seq of each row, increasing from First. Nil numbers
	// Rows contiguously from First. Only a kind that replaces stored rows may
	// skip seqs, since only there did the source leave them behind.
	Seqs []int64
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
	return i.EnsureAs(ctx, i.source, stream, stream)
}

// sourceStream is the stream an index stream mirrors: the backend holding it
// and its id there.
type sourceStream struct {
	Backend
	stream string
}

// EnsureAs is Ensure for stream of from, mirrored into the index as
// indexStream: how one index holds the streams of several sources that may
// share an id, each under an id of its own. Every index call names
// indexStream; only reads of the source name stream.
func (i *Indexer) EnsureAs(ctx context.Context, from Backend, stream, indexStream string) error {
	if i.same && (from != i.source || stream != indexStream) {
		return fmt.Errorf("index stream %q: an index that is its own source mirrors only its own streams, under their own ids", indexStream)
	}
	if err := ValidateStream(indexStream); err != nil {
		return err
	}
	unlock := i.locks.Lock(indexStream)
	defer unlock()
	origin := sourceStream{Backend: from, stream: stream}
	source, err := from.Meta(ctx, stream)
	if err != nil {
		return fmt.Errorf("index stream %q: %w", indexStream, err)
	}
	if err := source.Validate(); err != nil {
		return fmt.Errorf("index stream %q: source metadata: %w", indexStream, err)
	}
	source.Stream = indexStream
	stream = indexStream
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
	if indexed, err = i.catchUp(ctx, origin, source, indexed, found); err != nil {
		return err
	}
	latest, err := from.Meta(ctx, origin.stream)
	if err != nil {
		return fmt.Errorf("index stream %q: re-read source metadata: %w", stream, err)
	}
	latest.Stream = indexStream
	if latest.Generation != source.Generation {
		return fmt.Errorf("index stream %q changed generation from %q to %q while it was being indexed", stream, source.Generation, latest.Generation)
	}
	// Catch up again to the re-read snapshot: rows, trims and reopens the
	// source reported while the first pass ran are in the index before Ensure
	// returns.
	if indexed, err = i.catchUp(ctx, origin, latest, indexed, true); err != nil {
		return err
	}
	if indexed, err = i.mirrorCompaction(ctx, origin, latest, indexed); err != nil {
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
func (i *Indexer) catchUp(ctx context.Context, origin sourceStream, source, indexed Meta, found bool) (Meta, error) {
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
		if indexed.HighSeq, err = i.copyAfter(ctx, origin, source, high, !found); err != nil {
			return Meta{}, err
		}
		indexed.LowSeq = max(indexed.LowSeq, source.LowSeq)
	}
	if !found {
		if indexed, err = i.index.Meta(ctx, stream); err != nil {
			return Meta{}, fmt.Errorf("index stream %q: read the created index: %w", stream, err)
		}
	}
	return indexed, nil
}

// mirrorReopen reopens a sealed index whose source has resumed, so readers do
// not take the stale seal as the end of the stream. A sealed index short of
// the source's high seq is reopened even when the source is sealed again, so
// the rows it gained in between can be imported; mirrorSeal reseals it.
func (i *Indexer) mirrorReopen(ctx context.Context, source, indexed Meta) (Meta, error) {
	if !indexed.Sealed || (source.Sealed && indexed.HighSeq >= source.HighSeq) {
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

// mirrorCompaction drops the indexed rows a compaction of the source dropped,
// once the source's compaction count differs from the index's: the seqs the
// index holds that the source no longer does.
func (i *Indexer) mirrorCompaction(ctx context.Context, origin sourceStream, source, indexed Meta) (Meta, error) {
	if source.Compactions == indexed.Compactions {
		return indexed, nil
	}
	held := map[int64]bool{}
	if err := origin.Scan(ctx, origin.stream, 0, func(seq int64, _ Row) error {
		held[seq] = true
		return nil
	}); err != nil {
		return Meta{}, fmt.Errorf("index stream %q: read the compacted source: %w", source.Stream, err)
	}
	var dropped []int64
	if err := i.index.Scan(ctx, source.Stream, 0, func(seq int64, _ Row) error {
		if !held[seq] && seq <= source.HighSeq {
			dropped = append(dropped, seq)
		}
		return nil
	}); err != nil {
		return Meta{}, fmt.Errorf("index stream %q: read the index: %w", source.Stream, err)
	}
	mirrored, err := i.index.DeleteSeqs(ctx, source.Stream, dropped, source.Compactions)
	if err != nil {
		return Meta{}, fmt.Errorf("index stream %q: drop %d compacted rows: %w", source.Stream, len(dropped), err)
	}
	return mirrored, nil
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

// copyAfter imports every source row after high under its source seq,
// checking the seqs increase and reach the high seq the source reported; the
// index refuses a skipped seq unless the kind replaces stored rows. A source
// stream with no rows past high is imported as an empty stream when the index
// does not hold it yet (create). It returns the last seq the index holds after.
//
// A scan that ends early is refused rather than taken as the stream: an index
// that stopped short would page the stream as complete, and every later Ensure
// would find it already caught up.
func (i *Indexer) copyAfter(ctx context.Context, origin sourceStream, source Meta, high int64, create bool) (int64, error) {
	stream := source.Stream
	last := high
	var batch []Row
	var seqs []int64
	flush := func() error {
		expected := Window{From: last + 1, To: last}
		if len(seqs) > 0 {
			expected = Window{From: seqs[0], To: seqs[len(seqs)-1]}
		}
		window, err := i.index.Import(ctx, ImportRequest{Source: source, First: expected.From, Rows: batch, Seqs: seqs})
		if err != nil {
			return fmt.Errorf("index stream %q: %w", stream, err)
		}
		if window != expected {
			return fmt.Errorf("index stream %q: import returned window %+v, expected %+v", stream, window, expected)
		}
		batch, seqs = nil, nil
		return nil
	}
	err := origin.Scan(ctx, origin.stream, high, func(seq int64, row Row) error {
		if seq <= last {
			return fmt.Errorf("source stream %q returned seq %d after seq %d", stream, seq, last)
		}
		batch, seqs, last = append(batch, row), append(seqs, seq), seq
		if len(batch) < indexBatch {
			return nil
		}
		return flush()
	})
	if err != nil {
		return 0, fmt.Errorf("index stream %q: %w", stream, err)
	}
	if last < source.HighSeq {
		return 0, fmt.Errorf("index stream %q: the source holds rows through seq %d but its scan reached seq %d", stream, source.HighSeq, last)
	}
	if len(batch) > 0 || create {
		if err := flush(); err != nil {
			return 0, err
		}
	}
	return last, nil
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
