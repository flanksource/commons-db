package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Derived reports whether this file can discard indexed rows and rebuild them
// from a separate source.
func (b *Backend) Derived() bool { return b.derived }

// Prepare reconciles source's table before comparing the indexed stream. A
// derived index removes an older incarnation so its first import starts at 1.
func (b *Backend) Prepare(ctx context.Context, source recordstore.Meta) (recordstore.Meta, bool, error) {
	if err := source.Validate(); err != nil {
		return recordstore.Meta{}, false, err
	}
	unlock := b.locks.Lock(source.Stream)
	defer unlock()
	if _, err := b.kindTable(source.Kind); err != nil {
		return recordstore.Meta{}, false, err
	}
	if err := b.purgeExpired(ctx, source.Stream); err != nil {
		return recordstore.Meta{}, false, err
	}
	indexed, err := b.Meta(ctx, source.Stream)
	if errors.Is(err, recordstore.ErrNotFound) {
		return recordstore.Meta{}, false, nil
	}
	if err != nil {
		return recordstore.Meta{}, false, err
	}
	if indexed.Generation == source.Generation {
		if indexed.Kind != source.Kind {
			return recordstore.Meta{}, false, fmt.Errorf("stream %q generation %q holds kind %q, not %q", source.Stream, source.Generation, indexed.Kind, source.Kind)
		}
		return indexed, true, nil
	}
	if !b.derived {
		return recordstore.Meta{}, false, fmt.Errorf("stream %q has generation %q, not source generation %q; its rows are durable and cannot be rebuilt", source.Stream, indexed.Generation, source.Generation)
	}
	if err := b.removeGeneration(ctx, source); err != nil {
		return recordstore.Meta{}, false, err
	}
	return recordstore.Meta{}, false, nil
}

func (b *Backend) removeGeneration(ctx context.Context, source recordstore.Meta) error {
	return b.database.Write(func(writer *sql.DB) error {
		return b.removeGenerationLocked(ctx, writer, source)
	})
}

func (b *Backend) removeGenerationLocked(ctx context.Context, writer *sql.DB, source recordstore.Meta) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("stream %q: replace generation: %w", source.Stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	indexed, err := readMeta(ctx, tx, source.Stream, b.now())
	if errors.Is(err, recordstore.ErrNotFound) {
		return nil
	}
	if err != nil {
		return err
	}
	if indexed.Generation == source.Generation {
		if indexed.Kind != source.Kind {
			return fmt.Errorf("stream %q generation %q holds kind %q, not %q", source.Stream, source.Generation, indexed.Kind, source.Kind)
		}
		return nil
	}
	if err := removeStreamTx(ctx, tx, source.Stream, indexed.Kind); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stream %q: replace generation: %w", source.Stream, err)
	}
	return nil
}

// Import stores rows under source's generation and seqs. The requested first
// seq must follow the indexed high seq exactly; a stream the index does not
// hold yet starts at it, since its source may have trimmed the seqs below.
func (b *Backend) Import(ctx context.Context, request recordstore.ImportRequest) (recordstore.Window, error) {
	if err := request.Source.Validate(); err != nil {
		return recordstore.Window{}, err
	}
	if request.First < 1 {
		return recordstore.Window{}, fmt.Errorf("stream %q: import must start at a seq of at least 1, got %d", request.Source.Stream, request.First)
	}
	unlock := b.locks.Lock(request.Source.Stream)
	defer unlock()
	if err := b.purgeExpired(ctx, request.Source.Stream); err != nil {
		return recordstore.Window{}, err
	}
	table, err := b.kindTable(request.Source.Kind)
	if err != nil {
		return recordstore.Window{}, err
	}
	stored, err := storedRows(table.Table, request.Source.Kind, request.Rows)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", request.Source.Stream, err)
	}
	var window recordstore.Window
	err = b.database.Write(func(writer *sql.DB) error {
		var err error
		window, err = b.commitImportLocked(ctx, writer, table, request, stored)
		return err
	})
	return window, err
}

func (b *Backend) commitImportLocked(ctx context.Context, writer *sql.DB, table kindTable, request recordstore.ImportRequest, stored []query.Row) (recordstore.Window, error) {
	stream := request.Source.Stream
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: begin: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	now := b.now()
	meta, err := b.importedStream(ctx, tx, request)
	if err != nil {
		return recordstore.Window{}, err
	}
	if request.First != meta.HighSeq+1 {
		return recordstore.Window{}, fmt.Errorf("stream %q: import starts at seq %d but the next seq is %d", stream, request.First, meta.HighSeq+1)
	}
	window := recordstore.Window{From: request.First, To: request.First + int64(len(stored)) - 1}
	if err := insertRows(ctx, tx, table, stream, window, stored, now); err != nil {
		return recordstore.Window{}, err
	}
	meta.Total += int64(len(stored))
	meta.HighSeq = window.To
	meta.UpdatedAt = request.Source.UpdatedAt
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = now
	}
	meta.Capped = request.Source.Capped
	if b.derived {
		meta.ExpiresAt = request.Source.ExpiresAt
	}
	if err := upsertMeta(ctx, tx, meta); err != nil {
		return recordstore.Window{}, err
	}
	if err := tx.Commit(); err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: commit: %w", stream, err)
	}
	return window, nil
}

// importedStream reads the indexed stream an import extends, or starts it
// empty just below the import's first seq.
func (b *Backend) importedStream(ctx context.Context, tx *sql.Tx, request recordstore.ImportRequest) (recordstore.Meta, error) {
	stream := request.Source.Stream
	meta, err := b.openStoredStream(ctx, tx, stream, b.now())
	if err == nil {
		if meta.Generation != request.Source.Generation {
			return recordstore.Meta{}, fmt.Errorf("stream %q has generation %q, not import generation %q", stream, meta.Generation, request.Source.Generation)
		}
		if meta.Kind != request.Source.Kind {
			return recordstore.Meta{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, meta.Kind, request.Source.Kind)
		}
		return meta, recordstore.RefuseSealed(meta)
	}
	if !errors.Is(err, recordstore.ErrNotFound) {
		return recordstore.Meta{}, err
	}
	meta = recordstore.Meta{
		Stream: stream, Kind: request.Source.Kind, Generation: request.Source.Generation,
		LowSeq: request.First, HighSeq: request.First - 1,
	}
	if b.derived {
		meta.ExpiresAt = request.Source.ExpiresAt
	} else if b.ttl > 0 {
		expires := b.now().Add(b.ttl)
		meta.ExpiresAt = &expires
	}
	return meta, nil
}

// TrimBelow drops the indexed rows below lowSeq, mirroring a trim of the
// source. An index that had not reached lowSeq is moved up to it as an empty
// stream, so the next import starts at lowSeq.
func (b *Backend) TrimBelow(ctx context.Context, stream string, lowSeq int64) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	var meta recordstore.Meta
	err := b.database.Write(func(writer *sql.DB) error {
		var err error
		meta, err = b.trimStreamLocked(ctx, writer, stream, func(tx *sql.Tx, table sqlitetable.Table, meta recordstore.Meta) (recordstore.Meta, error) {
			return trimBelowTx(ctx, tx, table, meta, lowSeq)
		})
		return err
	})
	return meta, err
}
