package db

import (
	"context"

	"github.com/exaring/otelpgx"
	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons-db/drivers"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/jackc/pgx/v5/stdlib"
)

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
