package app

import (
	"context"
	"fmt"
	"sync"

	"github.com/flanksource/commons-db/cmd/query/profiles"
	dbcontext "github.com/flanksource/commons-db/context"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

type Runtime struct {
	mu      sync.RWMutex
	db      *gorm.DB
	context dbcontext.Context
	store   profiles.Store

	dbOptions  DatabaseOptions
	connect    sync.Once
	connectErr error
	dsn        string
	pool       *pgxpool.Pool
	stop       func() error
}

func NewRuntime(ctx dbcontext.Context, fileStore profiles.Store, dbOptions DatabaseOptions) (*Runtime, error) {
	if ctx.Context.Context == nil {
		return nil, fmt.Errorf("query context is required")
	}
	if fileStore == nil {
		return nil, fmt.Errorf("profile store is required")
	}
	return &Runtime{context: ctx, store: fileStore, dbOptions: dbOptions}, nil
}

// SetDatabaseOptions replaces the options resolved from the command line. It
// must be called before anything opens the database — `serve` uses it to own
// the embedded cluster it starts.
func (r *Runtime) SetDatabaseOptions(options DatabaseOptions) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.dbOptions = options
}

// EnsureDatabase opens the configured database once and rebinds the context and
// profile store onto it. Connecting is deferred to the first caller that needs
// it so `query --help` never starts PostgreSQL.
func (r *Runtime) EnsureDatabase(ctx context.Context) error {
	r.mu.RLock()
	options := r.dbOptions
	r.mu.RUnlock()
	if !options.Enabled() {
		return fmt.Errorf("no database configured; --db is empty")
	}
	r.connect.Do(func() { r.connectErr = r.open(ctx, options) })
	return r.connectErr
}

func (r *Runtime) open(ctx context.Context, options DatabaseOptions) error {
	opened, err := openDatabase(ctx, options)
	if err != nil {
		return err
	}
	store, err := profiles.NewDBStore(opened.gorm)
	if err != nil {
		opened.pool.Close()
		_ = opened.stop()
		return err
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db, r.pool, r.stop, r.dsn = opened.gorm, opened.pool, opened.stop, opened.dsn
	r.context = r.context.WithDB(opened.gorm, opened.pool).WithConnectionString(opened.dsn)
	r.store = store
	return nil
}

// Close releases what EnsureDatabase opened. Reused and KeepRunning clusters are
// left running by design, so this is a no-op for them.
func (r *Runtime) Close() error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.pool != nil {
		r.pool.Close()
		r.pool = nil
	}
	if r.stop == nil {
		return nil
	}
	stop := r.stop
	r.stop = nil
	return stop()
}

func (r *Runtime) SetDatabase(db *gorm.DB) error {
	if db == nil {
		return fmt.Errorf("query database is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.db = db
	return nil
}

func (r *Runtime) Database() (*gorm.DB, error) {
	if db := r.database(); db != nil {
		return db, nil
	}
	if err := r.EnsureDatabase(context.Background()); err != nil {
		return nil, fmt.Errorf("connections require a database: %w", err)
	}
	db := r.database()
	if db == nil {
		return nil, fmt.Errorf("connections require a database; --db is empty")
	}
	return db, nil
}

func (r *Runtime) database() *gorm.DB {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.db
}

func (r *Runtime) ConnectionString() string {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.dsn
}

func (r *Runtime) SetContext(ctx dbcontext.Context) error {
	if ctx.Context.Context == nil {
		return fmt.Errorf("query context is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.db != nil {
		ctx = ctx.WithDB(r.db, r.pool)
		if r.dsn != "" {
			ctx = ctx.WithConnectionString(r.dsn)
		}
	}
	r.context = ctx
	return nil
}

func (r *Runtime) Context() dbcontext.Context {
	r.mu.RLock()
	defer r.mu.RUnlock()
	return r.context
}

func (r *Runtime) SetProfileStore(store profiles.Store) error {
	if store == nil {
		return fmt.Errorf("profile store is required")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.store = store
	return nil
}

// ProfileStore is the database-backed store whenever --db names one, and the
// YAML file store otherwise.
func (r *Runtime) ProfileStore() (profiles.Store, error) {
	r.mu.RLock()
	enabled, connected := r.dbOptions.Enabled(), r.db != nil
	r.mu.RUnlock()
	if enabled && !connected {
		if err := r.EnsureDatabase(context.Background()); err != nil {
			return nil, err
		}
	}
	r.mu.RLock()
	defer r.mu.RUnlock()
	if r.store == nil {
		return nil, fmt.Errorf("profile store is not initialized")
	}
	return r.store, nil
}

func DecodeBody(ctx context.Context, fallback map[string]any) (map[string]any, error) {
	return profiles.DecodeRequestBody(ctx, fallback)
}
