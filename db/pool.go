package db

import (
	"context"
	"database/sql"
	"time"

	"github.com/exaring/otelpgx"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons-db/drivers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

// PoolConfig bounds a database/sql pool. The zero value is deliberately not
// useful: an unbounded pool turns a burst of goroutines into a burst of
// backends contending for the same rows, and database/sql's default ceiling of
// two idle connections then reconnects for every one of them.
type PoolConfig struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxIdleTime time.Duration
	ConnMaxLifetime time.Duration
}

// DefaultPoolConfig mirrors the floor NewPgxPool already enforces. Idle equals
// open so a bursty workload reuses connections rather than reconnecting, and
// the lifetime cap keeps a long-lived process from pinning server-side state.
func DefaultPoolConfig() PoolConfig {
	return PoolConfig{
		MaxOpenConns:    20,
		MaxIdleConns:    20,
		ConnMaxIdleTime: 5 * time.Minute,
		ConnMaxLifetime: 30 * time.Minute,
	}
}

func (c PoolConfig) Apply(db *sql.DB) {
	db.SetMaxOpenConns(c.MaxOpenConns)
	db.SetMaxIdleConns(c.MaxIdleConns)
	db.SetConnMaxIdleTime(c.ConnMaxIdleTime)
	db.SetConnMaxLifetime(c.ConnMaxLifetime)
}

// NewPgxPool creates a new pgx connection pool with OpenTelemetry tracing
func NewPgxPool(connection string) (*pgxpool.Pool, error) {
	connection, err := getConnection(connection)
	if err != nil {
		return nil, err
	}

	config, err := pgxpool.ParseConfig(connection)
	if err != nil {
		return nil, err
	}

	config.ConnConfig.Tracer = otelpgx.NewTracer(
		otelpgx.WithIncludeQueryParameters(),
		otelpgx.WithTrimSQLInSpanName(),
		otelpgx.WithSpanNameFunc(func(stmt string) string {
			maxL := 80
			if len(stmt) < maxL {
				maxL = len(stmt)
			}
			return stmt[:maxL]
		}),
	)

	// prevent deadlocks from concurrent queries
	if config.MaxConns < 20 {
		config.MaxConns = 20
	}

	pool, err := pgxpool.NewWithConfig(context.Background(), config)
	if err != nil {
		return nil, err
	}

	row := pool.QueryRow(context.Background(), "SELECT pg_size_pretty(pg_database_size($1));", config.ConnConfig.Database)
	var size string
	if err := row.Scan(&size); err != nil {
		return nil, err
	}

	logger.Infof("Initialized DB: %s (%s)", config.ConnConfig.Host, size)
	return pool, nil
}

func getConnection(connection string) (string, error) {
	pgxConfig, err := drivers.ParseURL(connection)
	if err != nil {
		return connection, err
	} else if pgxConfig != nil {
		return stdlib.RegisterConnConfig(pgxConfig), nil
	}
	return connection, nil
}
