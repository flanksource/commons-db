// Package sqlite opens a file-backed SQLite database with a serialized writer,
// a query-only read pool, and optional buffered asynchronous writes.
package sqlite

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"maps"
	"net/url"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/flanksource/commons-db/db/sqlitetable"
	"github.com/flanksource/commons-db/query"
	_ "modernc.org/sqlite"
)

const (
	asyncWriteBufferInterval = 100 * time.Millisecond
	asyncWriteQueueCapacity  = 256
)

var (
	// ErrClosed is returned when a write is submitted after Close starts.
	ErrClosed = errors.New("sqlite database is closed")
	// ErrWriteQueueFull is returned when the asynchronous write queue is full.
	ErrWriteQueueFull = errors.New("sqlite asynchronous write queue is full")
	// ErrWriteErrorHandlerRequired is returned when an asynchronous write has no error handler.
	ErrWriteErrorHandlerRequired = errors.New("sqlite asynchronous writes require OnWriteError")
)

// Options configure a file-backed SQLite database.
type Options struct {
	// Path is the SQLite database file.
	Path string

	// OnWriteError receives failures from accepted asynchronous mutations.
	// It is called after the write lock is released.
	OnWriteError func(error)
}

// Mutation changes a SQLite database through its serialized writer.
type Mutation func(*sql.DB) error

type queuedMutation struct {
	name string
	run  Mutation
}

// DB owns separate SQLite read and write pools over one file.
type DB struct {
	writer *sql.DB
	reader *sql.DB
	path   string

	mutations sync.RWMutex
	queue     chan queuedMutation
	stop      chan struct{}
	done      chan struct{}

	stateMu sync.Mutex
	closing bool

	onWriteError func(error)
	closeOnce    sync.Once
	closeErr     error
}

// Open opens or creates options.Path and starts its asynchronous writer.
func Open(options Options) (*DB, error) {
	if strings.TrimSpace(options.Path) == "" {
		return nil, fmt.Errorf("sqlite: database path is required")
	}
	path, err := filepath.Abs(options.Path)
	if err != nil {
		return nil, fmt.Errorf("sqlite: resolve %s: %w", options.Path, err)
	}
	writer, err := openWriter(path)
	if err != nil {
		return nil, err
	}
	reader, err := openReader(path)
	if err != nil {
		return nil, errors.Join(err, writer.Close())
	}
	database := &DB{
		writer: writer, reader: reader, path: path, onWriteError: options.OnWriteError,
		queue: make(chan queuedMutation, asyncWriteQueueCapacity), stop: make(chan struct{}), done: make(chan struct{}),
	}
	go database.writeLoop()
	return database, nil
}

func openWriter(path string) (*sql.DB, error) {
	writer, err := sql.Open("sqlite", fileURI(path)+"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, fmt.Errorf("sqlite: open writer %s: %w", path, err)
	}
	writer.SetMaxOpenConns(1)
	writer.SetMaxIdleConns(1)
	if err := writer.PingContext(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite: open writer %s: %w", path, err), writer.Close())
	}
	return writer, nil
}

func openReader(path string) (*sql.DB, error) {
	reader, err := sql.Open("sqlite", readDSN(path))
	if err != nil {
		return nil, fmt.Errorf("sqlite: open read pool %s: %w", path, err)
	}
	reader.SetMaxOpenConns(10)
	reader.SetMaxIdleConns(5)
	if err := reader.PingContext(context.Background()); err != nil {
		return nil, errors.Join(fmt.Errorf("sqlite: open read pool %s: %w", path, err), reader.Close())
	}
	return reader, nil
}

// Reader returns the query-only read pool. Callers that require several
// statements to observe one stable mutation boundary must hold Lease as well.
func (d *DB) Reader() *sql.DB { return d.reader }

// Write waits for exclusive access and runs mutation immediately.
func (d *DB) Write(mutation Mutation) error {
	if mutation == nil {
		return fmt.Errorf("sqlite: write mutation is required")
	}
	d.mutations.Lock()
	defer d.mutations.Unlock()
	if d.isClosing() {
		return ErrClosed
	}
	return mutation(d.writer)
}

// WriteAsync queues mutation for an asynchronous batch buffered for 100
// milliseconds. It returns ErrWriteQueueFull rather than blocking.
func (d *DB) WriteAsync(mutation Mutation) error {
	return d.enqueue("write mutation", mutation)
}

// ExecAsync queues a parameterized SQL statement for asynchronous execution.
func (d *DB) ExecAsync(statement string, args ...any) error {
	if strings.TrimSpace(statement) == "" {
		return fmt.Errorf("sqlite: asynchronous SQL statement is required")
	}
	values := slices.Clone(args)
	return d.enqueue("execute statement", func(writer *sql.DB) error {
		_, err := writer.Exec(statement, values...)
		return err
	})
}

// InsertAsync queues one map-shaped row for insertion into table.
func (d *DB) InsertAsync(table string, values map[string]any) error {
	if strings.TrimSpace(table) == "" {
		return fmt.Errorf("sqlite: asynchronous insert table is required")
	}
	if len(values) == 0 {
		return fmt.Errorf("sqlite: asynchronous insert into %q requires values", table)
	}
	keys := slices.Sorted(maps.Keys(values))
	columns := make([]string, len(keys))
	markers := make([]string, len(keys))
	args := make([]any, len(keys))
	for index, key := range keys {
		columns[index] = sqlitetable.QuoteIdentifier(key)
		markers[index] = "?"
		args[index] = values[key]
	}
	statement := fmt.Sprintf("INSERT INTO %s (%s) VALUES (%s)",
		sqlitetable.QuoteIdentifier(table), strings.Join(columns, ", "), strings.Join(markers, ", "))
	return d.enqueue(fmt.Sprintf("insert into %q", table), func(writer *sql.DB) error {
		_, err := writer.Exec(statement, args...)
		return err
	})
}

// InsertTableAsync queues typed rows for insertion through sqlitetable.Table.
func (d *DB) InsertTableAsync(table sqlitetable.Table, rows []query.Row) error {
	if strings.TrimSpace(table.Name) == "" {
		return fmt.Errorf("sqlite: asynchronous typed insert table is required")
	}
	if len(rows) == 0 {
		return fmt.Errorf("sqlite: asynchronous typed insert into %q requires rows", table.Name)
	}
	table.Columns = slices.Clone(table.Columns)
	table.PrimaryKey = slices.Clone(table.PrimaryKey)
	table.Unique = slices.Clone(table.Unique)
	copied := make([]query.Row, len(rows))
	for index, row := range rows {
		copied[index] = maps.Clone(row)
	}
	return d.enqueue(fmt.Sprintf("insert typed rows into %q", table.Name), func(writer *sql.DB) error {
		return table.Insert(context.Background(), writer, copied)
	})
}

func (d *DB) enqueue(name string, mutation Mutation) error {
	if mutation == nil {
		return fmt.Errorf("sqlite: asynchronous write mutation is required")
	}
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	if d.closing {
		return ErrClosed
	}
	if d.onWriteError == nil {
		return ErrWriteErrorHandlerRequired
	}
	select {
	case d.queue <- queuedMutation{name: name, run: mutation}:
		return nil
	default:
		return ErrWriteQueueFull
	}
}

func (d *DB) writeLoop() {
	defer close(d.done)
	for {
		select {
		case first := <-d.queue:
			batch, stopping := d.bufferBatch(first)
			d.runBatch(batch)
			if stopping {
				return
			}
		case <-d.stop:
			d.runBatch(d.drainQueue(nil))
			return
		}
	}
}

func (d *DB) bufferBatch(first queuedMutation) ([]queuedMutation, bool) {
	batch := []queuedMutation{first}
	timer := time.NewTimer(asyncWriteBufferInterval)
	defer timer.Stop()
	for {
		select {
		case mutation := <-d.queue:
			batch = append(batch, mutation)
		case <-timer.C:
			return d.drainQueue(batch), false
		case <-d.stop:
			return d.drainQueue(batch), true
		}
	}
}

func (d *DB) drainQueue(batch []queuedMutation) []queuedMutation {
	for {
		select {
		case mutation := <-d.queue:
			batch = append(batch, mutation)
		default:
			return batch
		}
	}
}

func (d *DB) runBatch(batch []queuedMutation) {
	if len(batch) == 0 {
		return
	}
	d.mutations.Lock()
	var failures []error
	for _, mutation := range batch {
		if err := mutation.run(d.writer); err != nil {
			failures = append(failures, fmt.Errorf("sqlite asynchronous %s: %w", mutation.name, err))
		}
	}
	d.mutations.Unlock()
	for _, err := range failures {
		d.onWriteError(err)
	}
}

func (d *DB) isClosing() bool {
	d.stateMu.Lock()
	defer d.stateMu.Unlock()
	return d.closing
}

// Lease prevents mutations until the returned release function is called.
func (d *DB) Lease() func() {
	d.mutations.RLock()
	var once sync.Once
	return func() { once.Do(d.mutations.RUnlock) }
}

// Path returns the absolute database file path.
func (d *DB) Path() string { return d.path }

// ReadDSN returns a query-only SQLite DSN for the database file.
func (d *DB) ReadDSN() string { return readDSN(d.path) }

func readDSN(path string) string {
	return fileURI(path) + "?mode=ro&_pragma=query_only(1)&_pragma=busy_timeout(5000)"
}

func fileURI(path string) string {
	return (&url.URL{Scheme: "file", Path: filepath.ToSlash(path)}).String()
}

// Close drains accepted asynchronous writes and closes both pools.
func (d *DB) Close() error {
	d.closeOnce.Do(func() {
		d.stateMu.Lock()
		d.closing = true
		close(d.stop)
		d.stateMu.Unlock()
		<-d.done
		d.mutations.Lock()
		defer d.mutations.Unlock()
		d.closeErr = errors.Join(d.reader.Close(), d.writer.Close())
	})
	return d.closeErr
}
