// Package dbtest resolves the PostgreSQL database that integration tests run
// against.
//
// Tests do not choose between an embedded and an external server themselves;
// they call ForT / ForGinkgo (or Open, for a framework-agnostic handle) and get
// whichever the environment provides:
//
//	COMMONS_DB_URL            connect to this server instead of embedding one
//	COMMONS_DB_CREATE         "false" uses COMMONS_DB_URL as-is; anything else
//	                          (including unset) carves out a fresh database
//
// When COMMONS_DB_URL is unset, dbtest starts or reuses the persistent embedded
// PostgreSQL server under $TMPDIR/commons-db and checks out an isolated
// database from its cross-process pool.
//
// Supplying Options.Provisioner replaces the empty-database pool with a
// content-addressed PostgreSQL template. The first matching request prepares
// and seals the template; every request clones it, runs instance preparation,
// and drops only the clone during cleanup. Templates are retained for reuse and
// superseded templates older than 24 hours are removed when they are unlocked.
// Provisioners require database creation and are rejected when
// COMMONS_DB_CREATE=false.
package dbtest

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"sync"
	"time"

	dbcontext "github.com/flanksource/commons-db/context"
	commonsdb "github.com/flanksource/commons-db/db"
	"github.com/jackc/pgx/v5/pgxpool"
	_ "github.com/lib/pq" // database/sql driver for the "postgres" DSNs handed out here
	"gorm.io/gorm"
)

const (
	EnvURL    = "COMMONS_DB_URL"
	EnvCreate = "COMMONS_DB_CREATE"
)

// connectTimeout bounds the blocking Postgres calls made while resolving a test
// database, so an unreachable server fails the test instead of hanging it.
const connectTimeout = 30 * time.Second

// Options configures how a test database is resolved.
type Options struct {
	// Name seeds the database name. Required.
	Name string
	// DataDir overrides where an embedded server keeps its cluster. Ignored
	// when COMMONS_DB_URL is set. Defaults to $TMPDIR/commons-db.
	DataDir string
	// LogName labels the gorm SQL logger. Defaults to Name.
	LogName string
	// Provisioner prepares a reusable schema template and reconciles each
	// isolated database cloned from it. It cannot be used when
	// COMMONS_DB_CREATE=false.
	Provisioner Provisioner
}

// Provisioner describes the database content required by a test. Fingerprint
// identifies reusable template content, PrepareTemplate builds a missing
// template, and PrepareInstance reconciles every isolated clone.
type Provisioner interface {
	Fingerprint(context.Context) (string, error)
	PrepareTemplate(context.Context, string) error
	PrepareInstance(context.Context, string) error
}

// DB is a resolved test database. The handle accessors are lazy and memoised,
// so a test that only needs a DSN never opens a pool. They do not return
// errors: a database that cannot be reached is a dead test, so they abort
// through the failure handler the constructing adapter installed.
type DB struct {
	dsn    string
	unique string
	opts   Options
	fail   func(error)

	mu    sync.Mutex
	sqlDB *sql.DB
	gorm  *gorm.DB
	pool  *pgxpool.Pool

	closers []func() error
}

// DSN is the connection string for the resolved database.
func (d *DB) DSN() string { return d.dsn }

// Unique is a per-resolution suffix. PostgreSQL roles are cluster-global rather
// than per-database, so tests that CREATE ROLE must suffix the role name with
// this to stay isolated from concurrent runs sharing one server.
func (d *DB) Unique() string { return d.unique }

// SQL returns a database/sql handle on the lib/pq driver.
func (d *DB) SQL() *sql.DB {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.sqlDB != nil {
		return d.sqlDB
	}
	handle, err := sql.Open("postgres", d.dsn)
	if err != nil {
		d.fail(fmt.Errorf("open %s: %w", redact(d.dsn), err))
		return nil
	}
	ctx, cancel := context.WithTimeout(context.Background(), connectTimeout)
	defer cancel()
	if err := handle.PingContext(ctx); err != nil {
		_ = handle.Close()
		d.fail(fmt.Errorf("ping %s: %w", redact(d.dsn), err))
		return nil
	}
	d.sqlDB = handle
	d.closers = append(d.closers, handle.Close)
	return handle
}

// Gorm returns a gorm handle, opening the underlying pgx pool on first use.
func (d *DB) Gorm() *gorm.DB {
	d.connect()
	return d.gorm
}

// Pool returns the pgx pool, opening it on first use.
func (d *DB) Pool() *pgxpool.Pool {
	d.connect()
	return d.pool
}

// Context returns a commons-db context with the gorm handle and pool attached.
func (d *DB) Context() dbcontext.Context {
	d.connect()
	return dbcontext.New().WithDB(d.gorm, d.pool).WithConnectionString(d.dsn)
}

func (d *DB) connect() {
	d.mu.Lock()
	defer d.mu.Unlock()
	if d.gorm != nil {
		return
	}
	logName := d.opts.LogName
	if logName == "" {
		logName = d.opts.Name
	}
	gormDB, pool, err := commonsdb.SetupDB(d.dsn, logName)
	if err != nil {
		d.fail(fmt.Errorf("connect %s: %w", redact(d.dsn), err))
		return
	}
	d.gorm, d.pool = gormDB, pool
	d.closers = append(d.closers, func() error { pool.Close(); return nil })
}

// Open resolves a database according to the environment. The returned cleanup
// closes every handle the DB produced and drops the database if Open created
// one; callers own it.
//
// Handle accessors on the returned DB panic on failure. ForT and ForGinkgo
// install their framework's failure handler instead.
func Open(opts Options) (*DB, func() error, error) {
	return open(context.Background(), opts)
}

func open(ctx context.Context, opts Options) (*DB, func() error, error) {
	if opts.Name == "" {
		return nil, nil, errors.New("dbtest.Options.Name is required")
	}
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}

	db := &DB{
		opts:   opts,
		unique: uniqueSuffix(),
		fail:   func(err error) { panic(err) },
	}

	url := os.Getenv(EnvURL)
	switch {
	case url != "" && os.Getenv(EnvCreate) == "false":
		if opts.Provisioner != nil {
			return nil, nil, errors.New("dbtest: Provisioner cannot be used with COMMONS_DB_CREATE=false")
		}
		db.dsn = url
	case url != "":
		dsn, drop, err := createConfiguredScratch(ctx, url, opts, db.unique)
		if err != nil {
			return nil, nil, err
		}
		db.dsn = dsn
		db.closers = append(db.closers, drop)
	default:
		adminURL, err := startSharedEmbedded(opts.DataDir)
		if err != nil {
			return nil, nil, err
		}
		dsn, drop, err := createEmbeddedScratch(ctx, adminURL, opts, db.unique)
		if err != nil {
			return nil, nil, fmt.Errorf("check out ephemeral database: %w", err)
		}
		db.dsn = dsn
		db.closers = append(db.closers, drop)
	}

	return db, db.close, nil
}

func createConfiguredScratch(
	ctx context.Context,
	adminURL string,
	opts Options,
	unique string,
) (string, func() error, error) {
	if opts.Provisioner != nil {
		return acquireProvisionedScratch(ctx, adminURL, opts.Name, unique, opts.Provisioner, time.Now())
	}
	return createScratch(adminURL, opts.Name, unique)
}

func createEmbeddedScratch(
	ctx context.Context,
	adminURL string,
	opts Options,
	unique string,
) (string, func() error, error) {
	if opts.Provisioner != nil {
		return acquireProvisionedScratch(ctx, adminURL, opts.Name, unique, opts.Provisioner, time.Now())
	}
	resolutionContext, cancel := context.WithTimeout(ctx, connectTimeout)
	defer cancel()
	return acquirePooledScratch(resolutionContext, adminURL, opts.Name, unique, time.Now())
}

// close runs every closer in reverse order, so handles are released before the
// database they point at is dropped.
func (d *DB) close() error {
	d.mu.Lock()
	closers := d.closers
	d.closers = nil
	d.mu.Unlock()

	var errs []error
	for i := len(closers) - 1; i >= 0; i-- {
		if err := closers[i](); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}
