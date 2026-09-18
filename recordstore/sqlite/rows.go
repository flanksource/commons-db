package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// scanPage is how many rows one read of a stream takes. Each page's cursor is
// closed before its callbacks run; the transaction keeps all pages on the
// same snapshot without occupying the writer connection.
const scanPage = 1000

// storedRows are rows as their kind's table stores them. A key the kind does
// not declare is an error rather than a column quietly left behind.
func storedRows(table sqlitetable.Table, kind string, rows []recordstore.Row) ([]query.Row, error) {
	byName := make(map[string]query.ColumnDef, len(table.Columns))
	for _, column := range table.Columns[2:] {
		byName[column.Name] = column
	}
	stored := make([]query.Row, len(rows))
	for index, row := range rows {
		stored[index] = make(query.Row, len(row)+2)
		for key, value := range row {
			column, ok := byName[key]
			if !ok {
				return nil, fmt.Errorf("row %d key %q is not a column of kind %q", index, key, kind)
			}
			converted, err := storedValue(column, value)
			if err != nil {
				return nil, fmt.Errorf("row %d column %q: %w", index, key, err)
			}
			stored[index][key] = converted
		}
	}
	return stored, nil
}

func storedValue(column query.ColumnDef, value any) (any, error) {
	if number, ok := value.(json.Number); ok {
		if integer, err := number.Int64(); err == nil {
			return integer, nil
		}
		return number.Float64()
	}
	if column.Type != query.ColumnTypeDateTime || value == nil {
		return value, nil
	}
	switch typed := value.(type) {
	case time.Time:
		return sqlitetable.FormatTime(typed), nil
	case *time.Time:
		if typed == nil {
			return nil, nil
		}
		return sqlitetable.FormatTime(*typed), nil
	case string:
		parsed, err := time.Parse(time.RFC3339Nano, typed)
		if err != nil {
			return nil, fmt.Errorf("datetime %q is not RFC3339: %w", typed, err)
		}
		return sqlitetable.FormatTime(parsed), nil
	default:
		return nil, fmt.Errorf("datetime must be a time or an RFC3339 string, got %T", value)
	}
}

// Scan reads stream's rows after afterSeq in seq order.
func (b *Backend) Scan(ctx context.Context, stream string, afterSeq int64, fn func(int64, recordstore.Row) error) error {
	meta, err := b.Meta(ctx, stream)
	if err != nil {
		return err
	}
	table, err := b.Table(meta.Kind)
	if err != nil {
		return err
	}
	tx, err := b.database.Reader().BeginTx(ctx, &sql.TxOptions{ReadOnly: true})
	if err != nil {
		return fmt.Errorf("stream %q: begin scan: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	snapshot, err := readMeta(ctx, tx, stream, b.now())
	if err != nil {
		return err
	}
	if snapshot.Kind != meta.Kind || snapshot.Generation != meta.Generation {
		return fmt.Errorf("stream %q changed while its scan began", stream)
	}
	streamID, err := table.Physical(streamColumn)
	if err != nil {
		return err
	}
	seq, err := table.Physical(seqColumn)
	if err != nil {
		return err
	}
	statement := table.Select() + fmt.Sprintf(` WHERE %s = ? AND %s > ? ORDER BY %s LIMIT ?`, streamID, seq, seq)
	for {
		rows, err := b.scanPage(ctx, tx, table, statement, stream, afterSeq)
		if err != nil {
			return err
		}
		for _, row := range rows {
			seq, ok := row[seqColumn].(int64)
			if !ok {
				return fmt.Errorf("stream %q: seq %v is not an integer", stream, row[seqColumn])
			}
			delete(row, seqColumn)
			delete(row, streamColumn)
			if err := fn(seq, row); err != nil {
				return err
			}
			afterSeq = seq
		}
		if len(rows) < scanPage {
			break
		}
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stream %q: finish scan: %w", stream, err)
	}
	return nil
}

func (b *Backend) scanPage(ctx context.Context, tx *sql.Tx, table sqlitetable.Table, statement, stream string, afterSeq int64) ([]query.Row, error) {
	rows, err := tx.QueryContext(ctx, statement, stream, afterSeq, scanPage)
	if err != nil {
		return nil, fmt.Errorf("stream %q: scan: %w", stream, err)
	}
	defer func() { _ = rows.Close() }()
	page, err := db.ScanRows[query.Row](rows)
	if err != nil {
		return nil, fmt.Errorf("stream %q: scan: %w", stream, err)
	}
	for _, row := range page {
		if err := sqlitetable.DecodeStructured(table.Columns, row); err != nil {
			return nil, fmt.Errorf("stream %q: %w", stream, err)
		}
	}
	return page, nil
}

// Sweep removes every stream whose expiry has passed, rows included, and
// reports how many it removed.
func (b *Backend) Sweep(ctx context.Context) (int, error) {
	var count int
	err := b.database.Write(func(writer *sql.DB) error {
		expired, err := b.expiredStreams(ctx, writer)
		if err != nil {
			return err
		}
		for _, stream := range expired {
			if err := b.removeStream(ctx, writer, stream.id, stream.kind); err != nil {
				return err
			}
		}
		count = len(expired)
		return nil
	})
	return count, err
}

type expiredStream struct{ id, kind string }

func (b *Backend) expiredStreams(ctx context.Context, writer *sql.DB) ([]expiredStream, error) {
	rows, err := writer.QueryContext(ctx, `SELECT stream_id, kind FROM record_streams
		WHERE expires_at IS NOT NULL AND expires_at <= ?`, sqlitetable.FormatTime(b.now()))
	if err != nil {
		return nil, fmt.Errorf("sweep %s: %w", b.Path(), err)
	}
	defer func() { _ = rows.Close() }()
	var expired []expiredStream
	for rows.Next() {
		var stream expiredStream
		if err := rows.Scan(&stream.id, &stream.kind); err != nil {
			return nil, fmt.Errorf("sweep %s: %w", b.Path(), err)
		}
		expired = append(expired, stream)
	}
	return expired, rows.Err()
}

func (b *Backend) removeStream(ctx context.Context, writer *sql.DB, stream, kind string) error {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return fmt.Errorf("stream %q: begin removal: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	if err := removeStreamTx(ctx, tx, stream, kind); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stream %q: commit removal: %w", stream, err)
	}
	return nil
}

// removeStreamTx removes, in tx, stream's rows from its kind's table and every
// record of the stream.
func removeStreamTx(ctx context.Context, tx *sql.Tx, stream, kind string) error {
	table, err := storeTable(ctx, tx, kind)
	if err != nil {
		return fmt.Errorf("stream %q: %w", stream, err)
	}
	streamID, err := table.Physical(streamColumn)
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, fmt.Sprintf(`DELETE FROM %s WHERE %s = ?`, sqlitetable.QuoteIdentifier(table.Name), streamID), stream); err != nil {
		return fmt.Errorf("stream %q: remove rows: %w", stream, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_appends WHERE stream_id = ?`, stream); err != nil {
		return fmt.Errorf("stream %q: remove append times: %w", stream, err)
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM record_streams WHERE stream_id = ?`, stream); err != nil {
		return fmt.Errorf("stream %q: remove metadata: %w", stream, err)
	}
	return nil
}

type queryer interface {
	QueryRowContext(ctx context.Context, query string, args ...any) *sql.Row
}

func readMeta(ctx context.Context, database queryer, stream string, now time.Time) (recordstore.Meta, error) {
	meta, err := loadMeta(ctx, database, stream)
	if err != nil {
		return recordstore.Meta{}, err
	}
	if meta.Expired(now) {
		return recordstore.Meta{}, fmt.Errorf("stream %q expired at %s: %w", stream, meta.ExpiresAt, recordstore.ErrNotFound)
	}
	return meta, nil
}

func loadMeta(ctx context.Context, database queryer, stream string) (recordstore.Meta, error) {
	meta := recordstore.Meta{Stream: stream}
	var updated string
	var expires sql.NullString
	err := database.QueryRowContext(ctx,
		`SELECT generation, kind, total, low_seq, high_seq, updated_at, expires_at, capped, sealed FROM record_streams WHERE stream_id = ?`, stream).
		Scan(&meta.Generation, &meta.Kind, &meta.Total, &meta.LowSeq, &meta.HighSeq, &updated, &expires, &meta.Capped, &meta.Sealed)
	if errors.Is(err, sql.ErrNoRows) {
		return recordstore.Meta{}, fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound)
	}
	if err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: read meta: %w", stream, err)
	}
	if meta.UpdatedAt, err = time.Parse(sqlitetable.TimeLayout, updated); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: updated_at: %w", stream, err)
	}
	if expires.Valid {
		at, err := time.Parse(sqlitetable.TimeLayout, expires.String)
		if err != nil {
			return recordstore.Meta{}, fmt.Errorf("stream %q: expires_at: %w", stream, err)
		}
		meta.ExpiresAt = &at
	}
	if err := meta.Validate(); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: invalid metadata: %w", stream, err)
	}
	return meta, nil
}

func upsertMeta(ctx context.Context, tx *sql.Tx, meta recordstore.Meta) error {
	var expires any
	if meta.ExpiresAt != nil {
		expires = sqlitetable.FormatTime(*meta.ExpiresAt)
	}
	if err := meta.Validate(); err != nil {
		return fmt.Errorf("stream %q: refuse to write invalid metadata: %w", meta.Stream, err)
	}
	_, err := tx.ExecContext(ctx, `INSERT INTO record_streams (stream_id, generation, kind, total, low_seq, high_seq, updated_at, expires_at, capped, sealed)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
		ON CONFLICT (stream_id) DO UPDATE SET generation = excluded.generation, total = excluded.total, low_seq = excluded.low_seq,
			high_seq = excluded.high_seq, updated_at = excluded.updated_at, expires_at = excluded.expires_at, capped = excluded.capped, sealed = excluded.sealed`,
		meta.Stream, meta.Generation, meta.Kind, meta.Total, meta.LowSeq, meta.HighSeq, sqlitetable.FormatTime(meta.UpdatedAt), expires, meta.Capped, meta.Sealed)
	if err != nil {
		return fmt.Errorf("stream %q: write meta: %w", meta.Stream, err)
	}
	return nil
}
