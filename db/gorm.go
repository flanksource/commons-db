package db

import (
	"database/sql"
	"errors"
	"fmt"

	"github.com/flanksource/commons-db/internal/dbtarget"
	"github.com/flanksource/commons-db/tracing"
	gormsqlite "github.com/glebarez/sqlite"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
	_ "modernc.org/sqlite"
)

// NewGorm creates a GORM connection for a PostgreSQL or SQLite DSN.
func NewGorm(connection string, config *gorm.Config) (*gorm.DB, error) {
	target, err := dbtarget.Parse(connection)
	if err != nil {
		return nil, err
	}
	var sqlDB *sql.DB
	switch target.Dialect {
	case dbtarget.Postgres:
		sqlDB, err = NewDB(target.DSN)
	case dbtarget.SQLite:
		sqlDB, err = sql.Open("sqlite", target.DSN)
		if err == nil {
			sqlDB.SetMaxOpenConns(1)
			sqlDB.SetMaxIdleConns(1)
		}
	default:
		return nil, fmt.Errorf("unsupported database dialect %q", target.Dialect)
	}
	if err != nil {
		return nil, err
	}
	dialector := gorm.Dialector(gormpostgres.New(gormpostgres.Config{Conn: sqlDB}))
	if target.Dialect == dbtarget.SQLite {
		dialector = gormsqlite.Dialector{Conn: sqlDB}
	}
	gormDB, err := gorm.Open(dialector, config)
	if err != nil {
		return nil, errors.Join(err, sqlDB.Close())
	}

	if err := gormDB.Use(tracing.NewPlugin()); err != nil {
		return nil, errors.Join(fmt.Errorf("error setting up tracing: %w", err), sqlDB.Close())
	}

	if err := gormDB.Use(NewServerTimingPlugin()); err != nil {
		return nil, errors.Join(fmt.Errorf("error setting up server timing: %w", err), sqlDB.Close())
	}

	return gormDB, nil
}

// NewDB creates a new sql.DB connection with a bounded pool. Callers that need
// different bounds apply their own PoolConfig to the returned handle.
func NewDB(connection string) (*sql.DB, error) {
	conn, err := getConnection(connection)
	if err != nil {
		return nil, err
	}
	db, err := sql.Open("pgx", conn)
	if err != nil {
		return nil, err
	}
	DefaultPoolConfig().Apply(db)
	return db, nil
}
