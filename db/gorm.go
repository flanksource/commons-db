package db

import (
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/tracing"
	gormpostgres "gorm.io/driver/postgres"
	"gorm.io/gorm"
)

// NewGorm creates a new Gorm DB connection using the provided connection string
func NewGorm(connection string, config *gorm.Config) (*gorm.DB, error) {
	db, err := NewDB(connection)
	if err != nil {
		return nil, err
	}

	gormDB, err := gorm.Open(
		gormpostgres.New(gormpostgres.Config{Conn: db}),
		config,
	)
	if err != nil {
		return nil, err
	}

	if err := gormDB.Use(tracing.NewPlugin()); err != nil {
		return nil, fmt.Errorf("error setting up tracing: %w", err)
	}

	return gormDB, nil
}

// NewDB creates a new sql.DB connection
func NewDB(connection string) (*sql.DB, error) {
	conn, err := getConnection(connection)
	if err != nil {
		return nil, err
	}
	return sql.Open("pgx", conn)
}
