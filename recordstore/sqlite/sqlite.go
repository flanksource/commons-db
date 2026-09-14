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

	// Schema resolves a kind to its columns. A kind it refuses cannot be
	// written.
	Schema func(kind string) ([]query.ColumnDef, error)

	// TTL is how long a stream lives from its first append unless Expire moves
	// it. Zero keeps a stream until something expires it.
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
	schema   func(string) ([]query.ColumnDef, error)
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
	tables   map[string]sqlitetable.Table
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
		now: now, tables: map[string]sqlitetable.Table{},
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

// Append numbers rows after the stream's high seq.
func (b *Backend) Append(ctx context.Context, stream, kind string, rows []recordstore.Row) (recordstore.Window, error) {
	return b.write(ctx, stream, kind, 0, rows)
}

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
	if _, err := b.Table(source.Kind); err != nil {
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
	var table string
	if err := tx.QueryRowContext(ctx, `SELECT k.table_name FROM record_streams s JOIN record_kinds k ON k.kind = s.kind WHERE s.stream_id = ?`, source.Stream).Scan(&table); err != nil {
		return fmt.Errorf("stream %q: read table while replacing generation: %w", source.Stream, err)
	}
	if err := removeStreamTx(ctx, tx, source.Stream, table); err != nil {
		return err
	}
	if err := tx.Commit(); err != nil {
		return fmt.Errorf("stream %q: replace generation: %w", source.Stream, err)
	}
	return nil
}

// Import stores rows under source's generation and seqs. The requested first
// seq must follow the indexed high seq exactly.
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
	table, err := b.Table(request.Source.Kind)
	if err != nil {
		return recordstore.Window{}, err
	}
	stored, err := storedRows(table, request.Source.Kind, request.Rows)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", request.Source.Stream, err)
	}
	return b.commitImport(ctx, table, request, stored)
}

func (b *Backend) commitImport(ctx context.Context, table sqlitetable.Table, request recordstore.ImportRequest, stored []query.Row) (recordstore.Window, error) {
	var window recordstore.Window
	err := b.database.Write(func(writer *sql.DB) error {
		var err error
		window, err = b.commitImportLocked(ctx, writer, table, request, stored)
		return err
	})
	return window, err
}

func (b *Backend) commitImportLocked(ctx context.Context, writer *sql.DB, table sqlitetable.Table, request recordstore.ImportRequest, stored []query.Row) (recordstore.Window, error) {
	stream := request.Source.Stream
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: begin: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	meta, err := b.openStoredStream(ctx, tx, stream, b.now())
	if errors.Is(err, recordstore.ErrNotFound) {
		meta = recordstore.Meta{
			Stream: stream, Kind: request.Source.Kind, Generation: request.Source.Generation,
			UpdatedAt: request.Source.UpdatedAt, Capped: request.Source.Capped,
		}
		if meta.UpdatedAt.IsZero() {
			meta.UpdatedAt = b.now()
		}
		if b.derived {
			meta.ExpiresAt = request.Source.ExpiresAt
		} else if b.ttl > 0 {
			expires := b.now().Add(b.ttl)
			meta.ExpiresAt = &expires
		}
	} else if err != nil {
		return recordstore.Window{}, err
	} else {
		if meta.Generation != request.Source.Generation {
			return recordstore.Window{}, fmt.Errorf("stream %q has generation %q, not import generation %q", stream, meta.Generation, request.Source.Generation)
		}
		if meta.Kind != request.Source.Kind {
			return recordstore.Window{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, meta.Kind, request.Source.Kind)
		}
	}
	if request.First != meta.HighSeq+1 {
		return recordstore.Window{}, fmt.Errorf("stream %q: import starts at seq %d but the next seq is %d", stream, request.First, meta.HighSeq+1)
	}
	window := recordstore.Window{From: request.First, To: request.First + int64(len(stored)) - 1}
	for index, row := range stored {
		row[streamColumn], row[seqColumn] = stream, window.From+int64(index)
	}
	if err := table.Insert(ctx, tx, stored); err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	meta.Total += int64(len(stored))
	meta.HighSeq = window.To
	meta.UpdatedAt = request.Source.UpdatedAt
	if meta.UpdatedAt.IsZero() {
		meta.UpdatedAt = b.now()
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

// write appends rows at first, or after the high seq when first is 0.
func (b *Backend) write(ctx context.Context, stream, kind string, first int64, rows []recordstore.Row) (recordstore.Window, error) {
	if err := recordstore.ValidateAppend(stream, kind); err != nil {
		return recordstore.Window{}, err
	}
	unlock := b.locks.Lock(stream)
	defer unlock()
	if err := b.purgeExpired(ctx, stream); err != nil {
		return recordstore.Window{}, err
	}
	// The stream's own kind is checked before the new kind is resolved, so a
	// write under the wrong kind says so rather than that the kind is unknown.
	if existing, err := b.Meta(ctx, stream); err == nil && existing.Kind != kind {
		return recordstore.Window{}, fmt.Errorf("stream %q holds kind %q, not %q", stream, existing.Kind, kind)
	}
	table, err := b.Table(kind)
	if err != nil {
		return recordstore.Window{}, err
	}
	stored, err := storedRows(table, kind, rows)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	return b.commitRows(ctx, table, stream, kind, first, stored)
}

// commitRows numbers stored rows after the stream's high seq, or checks they
// start at first when it is set, and commits them with the stream's metadata.
func (b *Backend) commitRows(ctx context.Context, table sqlitetable.Table, stream, kind string, first int64, stored []query.Row) (recordstore.Window, error) {
	var window recordstore.Window
	err := b.database.Write(func(writer *sql.DB) error {
		var err error
		window, err = b.commitRowsLocked(ctx, writer, table, stream, kind, first, stored)
		return err
	})
	return window, err
}

func (b *Backend) commitRowsLocked(ctx context.Context, writer *sql.DB, table sqlitetable.Table, stream, kind string, first int64, stored []query.Row) (recordstore.Window, error) {
	tx, err := writer.BeginTx(ctx, nil)
	if err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: begin: %w", stream, err)
	}
	defer func() { _ = tx.Rollback() }()
	now := b.now()
	meta, err := b.openStream(ctx, tx, stream, kind, now)
	if err != nil {
		return recordstore.Window{}, err
	}
	if first != 0 && first != meta.HighSeq+1 {
		return recordstore.Window{}, fmt.Errorf("stream %q: import starts at seq %d but the next seq is %d", stream, first, meta.HighSeq+1)
	}
	window := recordstore.Window{From: meta.HighSeq + 1, To: meta.HighSeq + int64(len(stored))}
	for index, row := range stored {
		row["stream_id"], row["seq"] = stream, window.From+int64(index)
	}
	if err := table.Insert(ctx, tx, stored); err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: %w", stream, err)
	}
	meta.Total += int64(len(stored))
	meta.HighSeq, meta.UpdatedAt = window.To, now
	if err := upsertMeta(ctx, tx, meta); err != nil {
		return recordstore.Window{}, err
	}
	if err := tx.Commit(); err != nil {
		return recordstore.Window{}, fmt.Errorf("stream %q: commit: %w", stream, err)
	}
	return window, nil
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
	return meta, nil
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
