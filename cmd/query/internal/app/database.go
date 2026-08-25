package app

import (
	"context"
	"fmt"
	"strings"

	"github.com/flanksource/commons-db/db"
	"github.com/jackc/pgx/v5/pgxpool"
	"gorm.io/gorm"
)

// DatabaseOptions is how every entry point — the CLI sub-commands and `serve` —
// names the same store. URL is the resolved --db value.
type DatabaseOptions struct {
	// URL is empty for no database, EmbeddedDatabase for the cluster under
	// DataDir, or a PostgreSQL DSN.
	URL string
	// DataDir backs EmbeddedDatabase and is ignored for a DSN.
	DataDir string
	// KeepEmbeddedRunning leaves a cluster this process started running once the
	// returned stop is called. Sub-commands set it so the next invocation reuses
	// the cluster instead of paying another cold start; `serve` owns its cluster
	// for the length of the process and does not.
	KeepEmbeddedRunning bool
}

func (o DatabaseOptions) Enabled() bool { return strings.TrimSpace(o.URL) != "" }

func (o DatabaseOptions) embedded() bool { return strings.TrimSpace(o.URL) == EmbeddedDatabase }

// ResolveDatabaseOptions resolves --db and --data-dir straight off the raw
// arguments, for the same reason ResolveConfigDir does.
func ResolveDatabaseOptions(args []string) DatabaseOptions {
	return DatabaseOptions{
		URL:                 ResolveDBURL(args),
		DataDir:             ResolveDataDir(args),
		KeepEmbeddedRunning: true,
	}
}

type openedDatabase struct {
	dsn  string
	gorm *gorm.DB
	pool *pgxpool.Pool
	stop func() error
}

func openDatabase(ctx context.Context, options DatabaseOptions) (openedDatabase, error) {
	if !options.Enabled() {
		return openedDatabase{}, fmt.Errorf("database url is required")
	}
	opened := openedDatabase{dsn: strings.TrimSpace(options.URL), stop: func() error { return nil }}
	if options.embedded() {
		dsn, stop, err := db.StartEmbedded(db.EmbeddedConfig{
			DataDir: options.DataDir, KeepRunning: options.KeepEmbeddedRunning,
		})
		if err != nil {
			return openedDatabase{}, fmt.Errorf("start embedded postgres: %w", err)
		}
		opened.dsn, opened.stop = dsn, stop
	}
	gdb, pool, err := db.SetupDB(opened.dsn, "query")
	if err != nil {
		_ = opened.stop()
		return openedDatabase{}, fmt.Errorf("setup db: %w", err)
	}
	if err := migrateSchema(ctx, opened.dsn); err != nil {
		pool.Close()
		_ = opened.stop()
		return openedDatabase{}, err
	}
	opened.gorm, opened.pool = gdb, pool
	return opened, nil
}
