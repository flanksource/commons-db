package sqlinspect

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/connection"
	inspection "github.com/flanksource/commons-db/inspect"
	native "github.com/flanksource/sqlglot-go/database/inspect"
)

const (
	DefaultMaxRelations       = native.DefaultMaxRelations
	DefaultMaxColumns         = native.DefaultMaxColumns
	DefaultMaxMetadata        = native.DefaultMaxMetadata
	DefaultMaxRoutines        = native.DefaultMaxRoutines
	DefaultMaxDefinitionBytes = native.DefaultMaxDefinitionBytes
)

type Limits = native.Limits
type Schema = native.Schema
type Relation = native.Relation
type Column = native.Column
type Index = native.Index
type ForeignKey = native.ForeignKey
type Trigger = native.Trigger
type Routine = native.Routine
type RoutineParameter = native.RoutineParameter

type Catalog struct {
	Driver         string                         `json:"driver"`
	Database       string                         `json:"database,omitempty"`
	Capabilities   connection.BackendCapabilities `json:"capabilities"`
	Databases      []string                       `json:"databases,omitempty"`
	DefaultSchema  string                         `json:"defaultSchema,omitempty"`
	Schemas        []Schema                       `json:"schemas"`
	Truncated      bool                           `json:"truncated,omitempty"`
	TruncateReason string                         `json:"truncateReason,omitempty"`
	Cache          *inspection.CacheMetadata      `json:"cache,omitempty"`
}

func ListDatabases(ctx context.Context, db *sql.DB, driver string) ([]string, error) {
	return native.ListDatabases(ctx, db, driver)
}

func inspect(ctx context.Context, db *sql.DB, driver string, limits Limits) (Catalog, error) {
	value, err := native.Inspect(ctx, db, driver, limits)
	return Catalog{Driver: value.Driver, Database: value.Database, Capabilities: value.Capabilities,
		Databases: value.Databases, DefaultSchema: value.DefaultSchema, Schemas: value.Schemas,
		Truncated: value.Truncated, TruncateReason: value.TruncateReason}, err
}
