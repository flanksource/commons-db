// A backend opened read-only: it reads a file another process writes, hands
// every mutation to that process as a batch, and can be promoted to write.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"time"

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
	if _, err := b.planAppend(table, stream, rows); err != nil {
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
func (b *Backend) adoptReadOnly(ctx context.Context, kind string, table kindTable) (kindTable, error) {
	adopted, missing, err := b.readTable(ctx, kind, table)
	if err != nil || !missing {
		return adopted, err
	}
	if b.submit == nil {
		return kindTable{}, fmt.Errorf("kind %q has no table with every declared column: %w", kind, b.errNoSubmitter())
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

// readTable adopts table from the catalog without writing, reporting it
// missing when the file has no table for the kind or lacks a declared column.
func (b *Backend) readTable(ctx context.Context, kind string, table kindTable) (kindTable, bool, error) {
	tx, err := b.database.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return kindTable{}, false, fmt.Errorf("kind %q: begin read: %w", kind, err)
	}
	defer func() { _ = tx.Rollback() }()
	var stored string
	err = tx.QueryRowContext(ctx, `SELECT columns FROM record_kinds WHERE kind = ?`, kind).Scan(&stored)
	if errors.Is(err, sql.ErrNoRows) {
		return kindTable{}, true, nil
	}
	if err != nil {
		return kindTable{}, false, fmt.Errorf("kind %q: read catalog: %w", kind, err)
	}
	catalog, mismatch, err := readCatalog(ctx, tx, table.Name, stored)
	if err != nil {
		return kindTable{}, false, fmt.Errorf("kind %q: %w", kind, err)
	}
	var added []string
	if mismatch == "" {
		columns, found := table.adopt(catalog)
		for _, column := range columns {
			added = append(added, column.Name)
		}
		mismatch = found
	}
	if mismatch != "" {
		return kindTable{}, false, fmt.Errorf("kind %q was stored in %s with %s: %w", kind, b.Path(), mismatch, recordstore.ErrSchemaConflict)
	}
	if len(added) > 0 {
		return kindTable{}, true, nil
	}
	adopted, err := withKey(table)
	return adopted, false, err
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
	b.readOnly.Store(false)
	b.startSweeper(b.sweepInterval)
	return nil
}
