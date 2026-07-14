package app

import (
	"context"
	"embed"
	"fmt"

	"github.com/flanksource/commons-db/migrate"
)

//go:embed migrations/*
var querySchema embed.FS

func migrateSchema(ctx context.Context, connection string) error {
	if err := migrate.Apply(ctx, connection, querySchema, migrate.WithDir("migrations"), migrate.WithName("query")); err != nil {
		return fmt.Errorf("migrate query schema: %w", err)
	}
	return nil
}
