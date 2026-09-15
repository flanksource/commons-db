package sqlite

import (
	"context"
	"database/sql"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// Trim removes the rows appended before before.
func (b *Backend) Trim(ctx context.Context, stream string, before time.Time) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	var meta recordstore.Meta
	err := b.database.Write(func(writer *sql.DB) error {
		var err error
		meta, err = b.trimStreamLocked(ctx, writer, stream, func(tx *sql.Tx, table string, meta recordstore.Meta) (recordstore.Meta, error) {
			return trimTx(ctx, tx, table, meta, before)
		})
		return err
	})
	return meta, err
}

// trimStreamLocked runs trim over stream's live metadata and table in one
// transaction and commits the metadata it returns.
func (b *Backend) trimStreamLocked(ctx context.Context, writer *sql.DB, stream string,
	trim func(tx *sql.Tx, table string, meta recordstore.Meta) (recordstore.Meta, error),
) (recordstore.Meta, error) {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: begin trim: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	meta, err := b.openStoredStream(ctx, tx, stream, b.now())
	if err != nil {
		return recordstore.Meta{}, err
	}
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT table_name FROM record_kinds WHERE kind = ?`, meta.Kind).Scan(&table); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: read table: %w", stream, err)
	}
	if meta, err = trim(tx, table, meta); err != nil {
		return recordstore.Meta{}, err
	}
	if err := upsertMeta(ctx, tx, meta); err != nil {
		return recordstore.Meta{}, err
	}
	if err := tx.Commit(); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: commit trim: %w", stream, err)
	}
	return meta, nil
}

// trimTx removes, in tx, every append of meta's stream up to the last one made
// before before.
func trimTx(ctx context.Context, tx *sql.Tx, table string, meta recordstore.Meta, before time.Time) (recordstore.Meta, error) {
	var last sql.NullInt64
	if err := tx.QueryRowContext(ctx, `SELECT MAX(last_seq) FROM record_appends WHERE stream_id = ? AND appended_at < ?`,
		meta.Stream, sqlitetable.FormatTime(before)).Scan(&last); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: find appends before %s: %w", meta.Stream, before, err)
	}
	if !last.Valid {
		return meta, nil
	}
	return trimBelowTx(ctx, tx, table, meta, last.Int64+1)
}

// trimBelowTx removes, in tx, meta's rows below lowSeq and the append times
// that only described them.
func trimBelowTx(ctx context.Context, tx *sql.Tx, table string, meta recordstore.Meta, lowSeq int64) (recordstore.Meta, error) {
	if lowSeq <= meta.LowSeq {
		return meta, nil
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE "c0" = ? AND "c1" < ?`, sqlitetable.QuoteIdentifier(table)), meta.Stream, lowSeq); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: trim rows below seq %d: %w", meta.Stream, lowSeq, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_appends WHERE stream_id = ? AND last_seq < ?`, meta.Stream, lowSeq); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: trim append times below seq %d: %w", meta.Stream, lowSeq, err)
	}
	meta.LowSeq, meta.HighSeq = lowSeq, max(meta.HighSeq, lowSeq-1)
	meta.Total = meta.HighSeq - meta.LowSeq + 1
	return meta, nil
}

// unstoredRows is the append's rows whose keys the stream does not hold, read
// in the append's own transaction, and how many it skipped.
func unstoredRows(ctx context.Context, tx *sql.Tx, table kindTable, stream string, write appendWrite) ([]query.Row, int64, error) {
	if write.keys == nil {
		return write.stored, 0, nil
	}
	statement, err := tx.PrepareContext(ctx, fmt.Sprintf(`SELECT EXISTS(SELECT 1 FROM %s WHERE "c0" = ? AND %s = ?)`,
		sqlitetable.QuoteIdentifier(table.Name), table.key))
	if err != nil {
		return nil, 0, fmt.Errorf("stream %q: prepare key lookup: %w", stream, err)
	}
	defer func() { _ = statement.Close() }()
	stored := make(map[string]bool, len(write.keys))
	for _, key := range write.keys {
		var exists bool
		if err := statement.QueryRowContext(ctx, stream, key).Scan(&exists); err != nil {
			return nil, 0, fmt.Errorf("stream %q: look up key %q: %w", stream, key, err)
		}
		stored[key] = exists
	}
	kept, _, skipped := recordstore.Unstored(write.stored, write.keys, func(key string) bool { return stored[key] })
	return kept, skipped, nil
}

// insertRows stores rows under window's seqs and records when they were
// appended.
func insertRows(ctx context.Context, tx *sql.Tx, table kindTable, stream string, window recordstore.Window, rows []query.Row, now time.Time) error {
	if len(rows) == 0 {
		return nil
	}
	for index, row := range rows {
		row[streamColumn], row[seqColumn] = stream, window.From+int64(index)
	}
	if err := table.Insert(ctx, tx, rows); err != nil {
		return fmt.Errorf("stream %q: %w", stream, err)
	}
	if _, err := tx.ExecContext(ctx, `INSERT INTO record_appends (stream_id, last_seq, appended_at) VALUES (?, ?, ?)`,
		stream, window.To, sqlitetable.FormatTime(now)); err != nil {
		return fmt.Errorf("stream %q: record append time: %w", stream, err)
	}
	return nil
}
