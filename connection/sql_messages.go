package connection

import (
	"database/sql"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/jackc/pgx/v5/stdlib"
)

// SQLClientOptions controls runtime reporting without exposing driver types.
type SQLClientOptions struct {
	// OnMessage receives raw PostgreSQL notice text on the query's goroutine.
	OnMessage func(string)
}

func postgresMessageClient(dsn string, options SQLClientOptions) (*sql.DB, error) {
	config, err := pgx.ParseConfig(dsn)
	if err != nil {
		return nil, err
	}
	config.OnNotice = func(_ *pgconn.PgConn, notice *pgconn.Notice) {
		options.OnMessage(notice.Message)
	}
	return stdlib.OpenDB(*config), nil
}
