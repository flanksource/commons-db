// Package sqlite stores record streams in a SQLite file: a table per kind,
// keyed (stream_id, seq), with the kind's columns typed from its schema, and a
// record_streams table describing every stream.
//
// The same file is the query index a `sql` profile reads through ReadDSN, which
// is what makes a stream pageable, filterable and exportable by the native
// profile engine. Used as a durable backend it is the index already; behind
// another backend an Indexer keeps it caught up.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons/logger"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
	sqlitedb "github.com/flanksource/commons-db/sqlite"
)

// Options configure a sqlite backend.
type Options struct {
	// Path is the database file. It must be a file rather than memory: a
	// profile reads it through a connection of its own.
	Path string

	// Schema resolves a kind to its columns, key and retention. A kind it
	// refuses cannot be written.
	Schema recordstore.SchemaResolver

	// TTL is how long a stream lives from its first append unless Expire moves
	// it, or how long a row of a kind retaining rows lives from its own append.
	// Zero keeps a stream until something expires it, and refuses a kind
	// retaining rows.
	TTL time.Duration

	// Derived marks the file as an index rebuilt from a source. A kind whose
	// columns changed since its table was created is then dropped with every
	// stream it held, for an Indexer to refill; in a durable file the same
	// change is an error, because those rows exist nowhere else.
	Derived bool

	// Now is the clock streams are stamped and expired by. Nil is time.Now.
	Now func() time.Time

	// SweepInterval is how often the file removes the streams whose expiry
	// has passed, rows included, until it is closed. It is required: a stream
	// expires from a ttl or from the source an index mirrors, and a file that
	// never swept would keep every row it was ever given.
	SweepInterval time.Duration
}

// Backend is a recordstore.Backend over a SQLite file.
type Backend struct {
	database *sqlitedb.DB
	schema   recordstore.SchemaResolver
	ttl      time.Duration
	derived  bool
	now      func() time.Time
	locks    recordstore.StreamLocks

	// stopSweeper cancels the sweeper goroutine and swept reports it gone.
	stopSweeper context.CancelFunc
	swept       chan struct{}
	closeOnce   sync.Once
	closeErr    error

	tablesMu sync.Mutex
	tables   map[string]kindTable
}

var _ recordstore.Index = (*Backend)(nil)

// Open opens (creating when absent) the file at options.Path.
func Open(options Options) (*Backend, error) {
	if strings.TrimSpace(options.Path) == "" {
		return nil, fmt.Errorf("sqlite record store: a database path is required")
	}
	if options.Schema == nil {
		return nil, fmt.Errorf("sqlite record store: a schema resolver is required")
	}
	if options.TTL < 0 {
		return nil, fmt.Errorf("sqlite record store: stream ttl cannot be negative")
	}
	if options.SweepInterval <= 0 {
		return nil, fmt.Errorf("sqlite record store: a positive sweep interval is required, or expired streams are never removed")
	}
	database, err := sqlitedb.Open(sqlitedb.Options{Path: options.Path})
	if err != nil {
		return nil, fmt.Errorf("sqlite record store: %w", err)
	}
	now := options.Now
	if now == nil {
		now = time.Now
	}
	backend := &Backend{
		database: database, schema: options.Schema, ttl: options.TTL, derived: options.Derived,
		now: now, tables: map[string]kindTable{},
	}
	if err := backend.createCatalog(context.Background()); err != nil {
		return nil, errors.Join(err, database.Close())
	}
	backend.startSweeper(options.SweepInterval)
	return backend, nil
}

// startSweeper sweeps the file every interval until Close. A sweep that fails
// is logged and retried on the next tick: there is no caller to hand the error
// to, and giving up would stop removing expired streams for good.
func (b *Backend) startSweeper(interval time.Duration) {
	ctx, cancel := context.WithCancel(context.Background())
	b.stopSweeper, b.swept = cancel, make(chan struct{})
	go func() {
		defer close(b.swept)
		ticker := time.NewTicker(interval)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-ticker.C:
				if _, err := b.Sweep(ctx); err != nil && ctx.Err() == nil {
					logger.Errorf("sqlite record store %s: sweep: %v", b.Path(), err)
				}
			}
		}
	}()
}

// ReadDSN is the read-only DSN a profile connection reads the file through.
func (b *Backend) ReadDSN() string {
	return b.database.ReadDSN()
}

// Path is the database file.
func (b *Backend) Path() string { return b.database.Path() }

// Lease holds off every row and schema mutation until the returned function is
// called, so an external reader can issue multiple stable paging statements.
func (b *Backend) Lease() func() {
	return b.database.Lease()
}

// Append numbers the rows it keeps after the stream's high seq.
func (b *Backend) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.AppendResult, error) {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return recordstore.AppendResult{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	if err := b.purgeExpired(ctx, stream); err != nil {
		return recordstore.AppendResult{}, err
	}
	// The stream's own kind is checked before the new kind is resolved, so a
	// write under the wrong kind says so rather than that the kind is unknown.
	if existing, err := b.Meta(ctx, stream); err == nil && existing.Kind != kind {
		return recordstore.AppendResult{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, existing.Kind, kind)
	}
	table, err := b.kindTable(kind)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	write, err := b.planAppend(table, stream, rows)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	var result recordstore.AppendResult
	err = b.database.Write(func(writer *sql.DB) error {
		var err error
		result, err = b.commitAppendLocked(ctx, writer, table, stream, write)
		return err
	})
	return result, err
}

// appendWrite is an append checked against its kind before any transaction
// begins: the rows as the table stores them, their keys and the retention the
// write applies.
type appendWrite struct {
	stored    []query.Row
	keys      []string
	retention time.Duration
}

func (b *Backend) planAppend(table kindTable, stream string, rows []recordstore.Row) (appendWrite, error) {
	retention, err := table.schema.RetentionTTL(b.ttl)
	if err != nil {
		return appendWrite{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	keys, err := table.schema.RowKeys(rows)
	if err != nil {
		return appendWrite{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	stored, err := storedRows(table.Table, table.schema.Kind, rows)
	if err != nil {
		return appendWrite{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	return appendWrite{stored: stored, keys: keys, retention: retention}, nil
}

// commitAppendLocked trims what the kind's retention has let go, skips the rows
// whose keys the stream still holds, numbers the rest after the high seq and
// commits them with the stream's metadata, all in one transaction.
func (b *Backend) commitAppendLocked(ctx context.Context, writer *sql.DB, table kindTable, stream string, write appendWrite) (recordstore.AppendResult, error) {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return recordstore.AppendResult{}, fmt.Errorf("stream %q: begin: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	now := b.now()
	meta, err := b.openStream(ctx, tx, stream, table.schema.Kind, now)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	if write.retention > 0 {
		if meta, err = trimTx(ctx, tx, table.Name, meta, now.Add(-write.retention)); err != nil {
			return recordstore.AppendResult{}, err
		}
		expires := now.Add(write.retention)
		meta.ExpiresAt = &expires
	}
	stored, skipped, err := unstoredRows(ctx, tx, table, stream, write)
	if err != nil {
		return recordstore.AppendResult{}, err
	}
	window := recordstore.Window{From: meta.HighSeq + 1, To: meta.HighSeq + int64(len(stored))}
	if err := insertRows(ctx, tx, table, stream, window, stored, now); err != nil {
		return recordstore.AppendResult{}, err
	}
	meta.Total += int64(len(stored))
	meta.HighSeq, meta.UpdatedAt = window.To, now
	if err := upsertMeta(ctx, tx, meta); err != nil {
		return recordstore.AppendResult{}, err
	}
	if err := tx.Commit(); err != nil {
		return recordstore.AppendResult{}, fmt.Errorf("stream %q: commit: %w", stream, err)
	}
	return recordstore.AppendResult{Window: window, Skipped: skipped}, nil
}

// purgeExpired removes an expired stream nothing has swept yet, so a write
// starts it again at seq 1 rather than colliding with rows that still hold
// those seqs. It holds the write lock throughout, so no leased read loses its
// rows part way.
func (b *Backend) purgeExpired(ctx context.Context, stream string) error {
	return b.database.Write(func(writer *sql.DB) error {
		var table string
		err := writer.QueryRowContext(ctx, `SELECT k.table_name FROM record_streams s JOIN record_kinds k ON k.kind = s.kind
			WHERE s.stream_id = ? AND s.expires_at IS NOT NULL AND s.expires_at <= ?`, stream, sqlitetable.FormatTime(b.now())).Scan(&table)
		if errors.Is(err, sql.ErrNoRows) {
			return nil
		}
		if err != nil {
			return fmt.Errorf("stream %q: read expiry: %w", stream, err)
		}
		return b.removeStream(ctx, writer, stream, table)
	})
}

// openStream reads stream for a write, or starts it under kind.
func (b *Backend) openStream(ctx context.Context, tx *sql.Tx, stream, kind string, now time.Time) (recordstore.Meta, error) {
	meta, err := b.openStoredStream(ctx, tx, stream, now)
	if errors.Is(err, recordstore.ErrNotFound) {
		meta = recordstore.NewStreamMeta(stream, kind, now)
		if b.ttl > 0 {
			expires := now.Add(b.ttl)
			meta.ExpiresAt = &expires
		}
		return meta, nil
	}
	if err != nil {
		return recordstore.Meta{}, err
	}
	if meta.Kind != kind {
		return recordstore.Meta{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, meta.Kind, kind)
	}
	return meta, recordstore.RefuseSealed(meta)
}

// openStoredStream removes an expired incarnation in the caller's write
// transaction before a replacement starts at seq 1.
func (b *Backend) openStoredStream(ctx context.Context, tx *sql.Tx, stream string, now time.Time) (recordstore.Meta, error) {
	meta, err := loadMeta(ctx, tx, stream)
	if err != nil || !meta.Expired(now) {
		return meta, err
	}
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT table_name FROM record_kinds WHERE kind = ?`, meta.Kind).Scan(&table); err != nil {
		return recordstore.Meta{}, fmt.Errorf("stream %q: read expired kind: %w", stream, err)
	}
	if err := removeStreamTx(ctx, tx, stream, table); err != nil {
		return recordstore.Meta{}, err
	}
	return recordstore.Meta{}, fmt.Errorf("stream %q expired at %s: %w", stream, meta.ExpiresAt, recordstore.ErrNotFound)
}

// Meta describes stream.
func (b *Backend) Meta(ctx context.Context, stream string) (recordstore.Meta, error) {
	if err := recordstore.ValidateStream(stream); err != nil {
		return recordstore.Meta{}, err
	}
	return readMeta(ctx, b.database.Reader(), stream, b.now())
}

// Expire moves stream's expiry to ttl from now.
func (b *Backend) Expire(ctx context.Context, stream string, ttl time.Duration) error {
	if err := recordstore.ValidateTTL(ttl); err != nil {
		return err
	}
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	now := b.now()
	return b.database.Write(func(writer *sql.DB) error {
		result, err := writer.ExecContext(ctx,
			`UPDATE record_streams SET expires_at = ? WHERE stream_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
			sqlitetable.FormatTime(now.Add(ttl)), stream, sqlitetable.FormatTime(now))
		if err != nil {
			return fmt.Errorf("stream %q: expire: %w", stream, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected == 0 {
			return errors.Join(fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound), err)
		}
		return nil
	})
}

// Seal marks stream complete on its record_streams row. An index mirrors a
// sealed source through it once it holds every row the source does.
func (b *Backend) Seal(ctx context.Context, stream string) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	now := b.now()
	return b.database.Write(func(writer *sql.DB) error {
		result, err := writer.ExecContext(ctx,
			`UPDATE record_streams SET sealed = 1, updated_at = CASE WHEN sealed = 1 THEN updated_at ELSE ? END
				WHERE stream_id = ? AND (expires_at IS NULL OR expires_at > ?)`,
			sqlitetable.FormatTime(now), stream, sqlitetable.FormatTime(now))
		if err != nil {
			return fmt.Errorf("stream %q: seal: %w", stream, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected == 0 {
			return errors.Join(fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound), err)
		}
		return nil
	})
}

// SetExpiry makes an index expire at the source's exact deadline. A nil
// deadline keeps it for as long as its source exists.
func (b *Backend) SetExpiry(ctx context.Context, stream string, expiresAt *time.Time) error {
	if err := recordstore.ValidateStream(stream); err != nil {
		return err
	}
	var value any
	if expiresAt != nil {
		value = sqlitetable.FormatTime(*expiresAt)
	}
	return b.database.Write(func(writer *sql.DB) error {
		result, err := writer.ExecContext(ctx, `UPDATE record_streams SET expires_at = ? WHERE stream_id = ?`, value, stream)
		if err != nil {
			return fmt.Errorf("stream %q: set index expiry: %w", stream, err)
		}
		if affected, err := result.RowsAffected(); err != nil || affected == 0 {
			return errors.Join(fmt.Errorf("stream %q: %w", stream, recordstore.ErrNotFound), err)
		}
		return nil
	})
}

// Close stops the sweeper, waiting out a sweep in progress, and closes the
// file.
func (b *Backend) Close() error {
	b.closeOnce.Do(func() {
		b.stopSweeper()
		<-b.swept
		b.closeErr = b.database.Close()
	})
	return b.closeErr
}
