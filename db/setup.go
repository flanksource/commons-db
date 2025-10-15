package db

import (
	"context"
	"fmt"

	"github.com/flanksource/commons/logger"
	"github.com/flanksource/commons-db/api"
	dbContext "github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/gorm"
	"github.com/jackc/pgx/v5/pgxpool"
	gormDriver "gorm.io/gorm"
)

// SetupDB creates database connections (gorm.DB and pgxpool.Pool)
// Note: This does NOT run migrations - consumers should handle migrations separately
func SetupDB(connectionString string, logName string) (gormDB *gormDriver.DB, pgxPool *pgxpool.Pool, err error) {
	logger.Infof("Connecting to database")

	pgxPool, err = NewPgxPool(connectionString)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create pgx pool: %w", err)
	}

	conn, err := pgxPool.Acquire(context.TODO())
	if err != nil {
		return nil, nil, fmt.Errorf("failed to acquire connection: %w", err)
	}
	defer conn.Release()

	if err := conn.Ping(context.Background()); err != nil {
		return nil, nil, fmt.Errorf("error pinging database: %w", err)
	}

	cfg := DefaultGormConfig()
	if logName != "" {
		cfg.Logger = gorm.NewSqlLogger(logger.GetLogger(logName))
	}

	gormDB, err = NewGorm(connectionString, cfg)
	if err != nil {
		return nil, nil, fmt.Errorf("failed to create gorm connection: %w", err)
	}

	return gormDB, pgxPool, nil
}

// InitDB creates a context with database connections
// Note: Does NOT check for migrations - consumers should verify migrations separately
func InitDB(config api.Config) (*dbContext.Context, error) {
	db, pool, err := SetupDB(config.ConnectionString, config.LogName)
	if err != nil {
		return nil, err
	}

	ctx := dbContext.NewContext(context.Background()).
		WithDB(db, pool).
		WithConnectionString(config.ConnectionString)

	return &ctx, nil
}
