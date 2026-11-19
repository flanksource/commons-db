// A backend opened read-only: it reads a file another process writes, hands
// every mutation to that process as a batch, and can be promoted to write.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"slices"
	"time"

	"github.com/flanksource/commons/logger"
	"github.com/google/uuid"

	"github.com/flanksource/commons-db/recordstore"
	sqlitedb "github.com/flanksource/commons-db/sqlite"
)

// changePoll is how often a read-only backend checks the file for commits
// another process made.
const changePoll = 150 * time.Millisecond

// errNoSubmitter is a read-only backend's refusal of a mutation it has no
// Submitter to hand to.
func (b *Backend) errNoSubmitter() error {
	return fmt.Errorf("sqlite record store %s: no writer to submit to: %w", b.Path(), sqlitedb.ErrReadOnly)
}

// submitEntry hands one entry to the writer as a batch of its own, declaring
// schemas, and returns what the entry did there: its result, or the error the
// writer refused it with, which unwraps to the store's sentinel.
func (b *Backend) submitEntry(ctx context.Context, entry recordstore.BatchEntry, schemas ...recordstore.KindSchema) (recordstore.EntryResult, error) {
	if b.submit == nil {
		return recordstore.EntryResult{}, b.errNoSubmitter()
	}
	batch := recordstore.Batch{ID: uuid.NewString(), Schemas: schemas, Entries: []recordstore.BatchEntry{entry}}
	result, err := b.submit(ctx, batch)
	if err != nil {
		return recordstore.EntryResult{}, fmt.Errorf("stream %q: submit %s: %w", entry.Stream, entry.Op, err)
	}
	if len(result.Entries) != 1 {
		return recordstore.EntryResult{}, fmt.Errorf("stream %q: submitted %s answered with %d entry results", entry.Stream, entry.Op, len(result.Entries))
	}
	if refused := result.Entries[0].Error; refused != nil {
		return recordstore.EntryResult{}, refused
	}
	return result.Entries[0], nil
}

// submitAppend checks an append against what this process can see — the
// stream's kind and seal, the kind's table and the rows' columns and keys —
// before submitting it, so the writer is asked only for what it would store.
func (b *Backend) submitAppend(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if existing, err := b.Meta(ctx, stream); err == nil {
		if existing.Kind != kind {
			return recordstore.AppendResult{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, existing.Kind, kind)
		}
		if err := recordstore.RefuseSealed(existing); err != nil {
			return recordstore.AppendResult{}, err
		}
	}
	table, err := b.kindTable(kind)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	// A dynamic kind's new columns are the writer's to add; this process
	// checks only that the rows agree with the columns it has.
	if table.schema.Options.Dynamic {
		if _, err := inferColumns(table, rows); err != nil {
			return recordstore.AppendResult{}, err
		}
	} else if _, _, err := b.planAppend(context.Background(), table, stream, rows); err != nil {
		return recordstore.AppendResult{}, err
	}
	result, err := b.submitEntry(ctx, recordstore.BatchEntry{Op: recordstore.BatchAppend, Stream: stream, Kind: kind, Rows: rows}, table.schema)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	return *result.Append, nil
}

// adoptReadOnly adopts kind's table from the catalog the writer keeps. A
// table or a declared column the file does not have yet is declared by
// submitting a batch of no entries, and adopted once the writer made it.
// Without a Submitter the backend declares it through a writer of its own,
// opened for that one transaction.
func (b *Backend) adoptReadOnly(ctx context.Context, kind string, table kindTable) (kindTable, error) {
	adopted, missing, err := b.readTable(ctx, kind, table)
	if err != nil || !missing {
		return adopted, err
	}
	if b.submit == nil {
		return b.declareDetached(ctx, kind, table, adopted)
	}
	if _, err := b.submit(ctx, recordstore.Batch{ID: uuid.NewString(), Schemas: []recordstore.KindSchema{table.schema}}); err != nil {
		return kindTable{}, fmt.Errorf("kind %q: submit its declaration: %w", kind, err)
	}
	if adopted, missing, err = b.readTable(ctx, kind, table); err != nil {
		return kindTable{}, err
	}
	if missing {
		return kindTable{}, fmt.Errorf("kind %q: the writer did not create its table with every declared column", kind)
	}
	return adopted, nil
}

// declareDetached reconciles kind's table through a writer opened for this
// declaration alone and closed again, so the backend stays read-only. When the
// file cannot be written, a table it already has is read through the columns
// it has — a declared column it lacks holds no value in any row — and the
// backend writes nothing, so no row can be stored without that column.
func (b *Backend) declareDetached(ctx context.Context, kind string, table, readable kindTable) (kindTable, error) {
	declared, err := b.reconcileDetached(ctx, kind, table)
	if err == nil {
		return declared, nil
	}
	if readable.Name == "" {
		return kindTable{}, fmt.Errorf("kind %q has no table in %s and none could be declared: %w", kind, b.Path(), errors.Join(err, b.errNoSubmitter()))
	}
	logger.Warnf("sqlite record store %s: kind %q lacks declared columns and they could not be added (%v); reading the columns it has", b.Path(), kind, err)
	return readable, nil
}

func (b *Backend) reconcileDetached(ctx context.Context, kind string, table kindTable) (kindTable, error) {
	writer, err := sqlitedb.Open(sqlitedb.Options{Path: b.Path()})
	if err != nil {
		return kindTable{}, err
	}
	err = writer.Write(func(db *sql.DB) error {
		table, err = b.reconcileTableLocked(ctx, db, kind, table)
		return err
	})
	return table, errors.Join(err, writer.Close())
}

// readTable adopts table from the catalog without writing, reporting it
// missing when the file has no table for the kind, or lacks a declared column
// or an index the kind asks for. A table missing only declared columns or
// indexes is returned as far as the file has it: its columns minus the ones
// it lacks.
func (b *Backend) readTable(ctx context.Context, kind string, table kindTable) (kindTable, bool, error) {
	tx, err := b.database.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return kindTable{}, false, fmt.Errorf("kind %q: begin read: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	stored, indexes, found, err := kindEntry(ctx, tx, kind)
	if err != nil || !found {
		return kindTable{}, !found && err == nil, err
	}
	catalog, mismatch, err := readCatalog(ctx, tx, table.Name, stored, indexes)
	if err != nil {
		return kindTable{}, false, fmt.Errorf("kind %q: %w", kind, err)
	}
	var added []string
	if mismatch == "" {
		table = table.withInferred(catalog)
		columns, found := table.adopt(catalog)
		for _, column := range columns {
			added = append(added, column.Name)
		}
		mismatch = found
	}
	if mismatch != "" {
		return kindTable{}, false, fmt.Errorf("kind %q was stored in %s with %s: %w", kind, b.Path(), mismatch, recordstore.ErrSchemaConflict)
	}
	_, indexed := withIndexes(catalog.indexes, kindIndexes(table.schema))
	adopted, err := withKey(table.without(added))
	return adopted, len(added) > 0 || indexed, err
}

// without is the table minus the named declared columns.
func (table kindTable) without(names []string) kindTable {
	if len(names) == 0 {
		return table
	}
	table.Columns, table.StoredAs = slices.Clone(table.Columns), slices.Clone(table.StoredAs)
	for index := len(table.Columns) - 1; index >= 0; index-- {
		if slices.Contains(names, table.Columns[index].Name) {
			table.Columns = slices.Delete(table.Columns, index, index+1)
			table.StoredAs = slices.Delete(table.StoredAs, index, index+1)
		}
	}
	return table
}

// WatchChanges calls changed whenever a commit to the file lands, until ctx
// ends: the writer's, when this backend reads, and its own once it writes —
// which covers the batches it ingests on other processes' behalf, not only
// the appends a Notifier sees made through it.
func (b *Backend) WatchChanges(ctx context.Context, changed func()) error {
	return b.database.WatchDataVersion(ctx, changePoll, changed)
}

// Promote makes a read-only backend write the file itself, for the process
// that has become its writer: it opens the writer, checks the catalog, and
// starts sweeping. Promoting a backend that writes already does nothing.
func (b *Backend) Promote(ctx context.Context) error {
	b.lifecycle.Lock()
	defer b.lifecycle.Unlock()
	if !b.readOnly.Load() {
		return nil
	}
	if err := b.database.EnableWrites(); err != nil {
		return fmt.Errorf("sqlite record store %s: promote: %w", b.Path(), err)
	}
	if err := b.createCatalog(ctx); err != nil {
		return fmt.Errorf("sqlite record store %s: promote: %w", b.Path(), err)
	}
	// A table read without the columns the file lacked is reconciled again,
	// now that the backend can add them.
	b.tablesMu.Lock()
	b.tables = map[string]kindTable{}
	b.tablesMu.Unlock()
	b.readOnly.Store(false)
	b.startSweeper(b.sweepInterval)
	return nil
}
