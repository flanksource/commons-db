// AppendBatch: a batch applied in one writer transaction, a savepoint per
// entry, and recorded in the ledger by id in that same transaction.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/recordstore"
)

var _ recordstore.BatchAppender = (*Backend)(nil)

// plannedEntry is an entry checked before the transaction begins: the table
// and rows of an append, or the error that fails the entry without touching
// the file.
type plannedEntry struct {
	entry recordstore.BatchEntry
	table kindTable
	write appendWrite
	err   error
}

// AppendBatch applies batch's entries in order, each one committed or rolled
// back alone, and records the outcome under batch.ID. A batch id the ledger
// already holds is answered with the outcome it recorded, applying nothing.
// Rows are stamped as appended now, when the batch is applied, so a kind
// retaining rows keeps them from then rather than from when they were made.
func (b *Backend) AppendBatch(ctx context.Context, batch recordstore.Batch) (recordstore.BatchResult, error) {
	switch {
	case b.derived:
		return recordstore.BatchResult{}, fmt.Errorf("sqlite record store %s: batch %q: a derived index takes rows only from its Indexer", b.Path(), batch.ID)
	case batch.ID == "":
		return recordstore.BatchResult{}, errors.New("sqlite record store: a batch needs an id")
	case batch.Producer.Instance == "":
		return recordstore.BatchResult{}, fmt.Errorf("sqlite record store: batch %q names no producer instance", batch.ID)
	}
	tables, err := b.batchTables(ctx, batch)
	if err != nil {
		return recordstore.BatchResult{}, err
	}
	planned := make([]plannedEntry, len(batch.Entries))
	for index, entry := range batch.Entries {
		planned[index] = b.planEntry(entry, tables)
	}
	defer b.lockStreams(batch.Entries)()
	var result recordstore.BatchResult
	err = b.database.Write(func(writer *sql.DB) error {
		return inTx(ctx, writer, fmt.Sprintf("batch %q", batch.ID), func(tx *sql.Tx) error {
			recorded, found, err := readBatchOutcome(ctx, tx, batch.ID)
			if err != nil || found {
				result = recorded
				return err
			}
			now := b.now()
			result = recordstore.BatchResult{ID: batch.ID, Entries: make([]recordstore.EntryResult, len(planned))}
			for index, entry := range planned {
				if result.Entries[index], err = b.applyEntry(ctx, tx, index, entry, now); err != nil {
					return err
				}
			}
			return recordBatchTx(ctx, tx, batch.Producer, result, now)
		})
	})
	if err != nil {
		return recordstore.BatchResult{}, err
	}
	return result, nil
}

// planEntry checks entry against what it asks for, and for an append against
// its kind's table.
func (b *Backend) planEntry(entry recordstore.BatchEntry, tables map[string]batchTable) plannedEntry {
	planned := plannedEntry{entry: entry}
	if err := recordstore.ValidateStream(entry.Stream); err != nil {
		planned.err = invalidEntry{err}
		return planned
	}
	switch entry.Op {
	case recordstore.BatchAppend:
		if err := recordstore.ValidateAppend(entry.Stream, entry.Kind); err != nil {
			planned.err = invalidEntry{err}
			return planned
		}
		resolved := tables[entry.Kind]
		if resolved.err != nil {
			planned.err = resolved.err
			return planned
		}
		write, err := b.planAppend(resolved.table, entry.Stream, entry.Rows)
		if err != nil {
			planned.err = invalidEntry{err}
			return planned
		}
		planned.table, planned.write = resolved.table, write
	case recordstore.BatchExpire:
		if err := recordstore.ValidateTTL(entry.TTL); err != nil {
			planned.err = invalidEntry{err}
		}
	case recordstore.BatchReopen:
		if entry.Generation == "" {
			planned.err = invalidEntry{fmt.Errorf("stream %q: reopen requires a generation", entry.Stream)}
		}
	case recordstore.BatchSeal, recordstore.BatchTrim, recordstore.BatchDelete:
	default:
		planned.err = invalidEntry{fmt.Errorf("stream %q: unknown batch operation %q", entry.Stream, entry.Op)}
	}
	return planned
}

// lockStreams takes the lock of every stream entries name, in sorted order so
// two batches naming the same streams cannot deadlock, and returns the
// function releasing them.
func (b *Backend) lockStreams(entries []recordstore.BatchEntry) (unlock func()) {
	streams := make([]string, 0, len(entries))
	for _, entry := range entries {
		streams = append(streams, entry.Stream)
	}
	slices.Sort(streams)
	streams = slices.Compact(streams)
	unlocks := make([]func(), len(streams))
	for index, stream := range streams {
		unlocks[index] = b.locks.Lock(stream)
	}
	return func() {
		for _, unlock := range slices.Backward(unlocks) {
			unlock()
		}
	}
}

// applyEntry applies one planned entry under a savepoint. An error the entry
// caused rolls back only the entry and becomes its result; any other error is
// returned, failing the whole batch.
func (b *Backend) applyEntry(ctx context.Context, tx *sql.Tx, index int, entry plannedEntry, now time.Time) (recordstore.EntryResult, error) {
	if entry.err != nil {
		return recordstore.EntryResult{Error: recordstore.NewBatchError(entry.err)}, nil
	}
	if _, err := tx.ExecContext(ctx, `SAVEPOINT batch_entry`); err != nil {
		return recordstore.EntryResult{}, fmt.Errorf("batch entry %d: savepoint: %w", index, err)
	}
	result, err := b.applyEntryTx(ctx, tx, entry, now)
	if err != nil {
		if !entryCaused(err) {
			return recordstore.EntryResult{}, fmt.Errorf("batch entry %d: %w", index, err)
		}
		if _, rollback := tx.ExecContext(ctx, `ROLLBACK TO batch_entry`); rollback != nil {
			return recordstore.EntryResult{}, fmt.Errorf("batch entry %d: roll back: %w", index, rollback)
		}
		result = recordstore.EntryResult{Error: recordstore.NewBatchError(err)}
	}
	if _, err := tx.ExecContext(ctx, `RELEASE batch_entry`); err != nil {
		return recordstore.EntryResult{}, fmt.Errorf("batch entry %d: release savepoint: %w", index, err)
	}
	return result, nil
}

// entryCaused reports an error the entry's own request caused — a sentinel
// the store reports, or an invalid request — rather than one the file raised.
func entryCaused(err error) bool {
	return errors.As(err, new(invalidEntry)) || recordstore.NewBatchError(err).Code != recordstore.BatchErrorInvalid
}

func (b *Backend) applyEntryTx(ctx context.Context, tx *sql.Tx, planned plannedEntry, now time.Time) (recordstore.EntryResult, error) {
	entry := planned.entry
	if entry.Generation != "" && entry.Op != recordstore.BatchReopen {
		if err := b.fenceGeneration(ctx, tx, entry, now); err != nil {
			return recordstore.EntryResult{}, err
		}
	}
	switch entry.Op {
	case recordstore.BatchAppend:
		appended, err := b.appendTx(ctx, tx, planned.table, entry.Stream, planned.write, now)
		if err != nil {
			return recordstore.EntryResult{}, err
		}
		if entry.Seal {
			if err := sealTx(ctx, tx, entry.Stream, now); err != nil {
				return recordstore.EntryResult{}, err
			}
		}
		return recordstore.EntryResult{Append: &appended}, nil
	case recordstore.BatchSeal:
		return recordstore.EntryResult{}, sealTx(ctx, tx, entry.Stream, now)
	case recordstore.BatchExpire:
		return recordstore.EntryResult{}, expireTx(ctx, tx, entry.Stream, entry.TTL, now)
	case recordstore.BatchReopen:
		return recordstore.EntryResult{}, reopenTx(ctx, tx, entry.Stream, entry.Generation, now)
	case recordstore.BatchTrim:
		meta, err := b.trimStreamTx(ctx, tx, entry.Stream, func(tx *sql.Tx, table sqlitetable.Table, meta recordstore.Meta) (recordstore.Meta, error) {
			return trimTx(ctx, tx, table, meta, entry.Before)
		})
		if err != nil {
			return recordstore.EntryResult{}, err
		}
		return recordstore.EntryResult{Meta: &meta}, nil
	default: // recordstore.BatchDelete; planEntry refused every other op
		meta, err := loadMeta(ctx, tx, entry.Stream)
		if err != nil {
			return recordstore.EntryResult{}, err
		}
		return recordstore.EntryResult{}, removeStreamTx(ctx, tx, entry.Stream, meta.Kind)
	}
}

// fenceGeneration refuses an entry naming a generation other than the one
// its stream holds as not found: the incarnation it was meant for is gone.
func (b *Backend) fenceGeneration(ctx context.Context, tx *sql.Tx, entry recordstore.BatchEntry, now time.Time) error {
	meta, err := b.openStoredStream(ctx, tx, entry.Stream, now)
	if err != nil {
		return err
	}
	if meta.Generation != entry.Generation {
		return fmt.Errorf("stream %q holds generation %q, not %q: %w", entry.Stream, meta.Generation, entry.Generation, recordstore.ErrNotFound)
	}
	return nil
}
