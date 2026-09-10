package inspector

import (
	"context"
	"database/sql"
	"fmt"
)

// Inspector provides database introspection capabilities.
//
// Every catalogue method is schema-scoped: it returns a bounded set of rows
// in one round trip, and each row carries the name of the object that owns it
// (Column.TableName, Index.TableName, ForeignKey.TableName, ProcParam.ProcName).
// Callers group; dialects do not. Asking per object turned a schema dump into
// thousands of round trips for no extra information.
//
// Ordering is part of the contract, not an accident: every method returns rows
// ordered by owner and then by the object's own natural order (ordinal position,
// index name, constraint column position, parameter id). Consumers cache and
// compare catalogs, so row ordering must be deterministic.
type Inspector interface {
	// Schema operations
	GetSchemas(ctx context.Context) ([]string, error)
	GetDefaultSchema(ctx context.Context) (string, error)
	GetDatabaseName(ctx context.Context) (string, error)

	// Table operations
	GetTables(ctx context.Context, schema string, tableType string) ([]*Table, error)
	GetColumns(ctx context.Context, schema string) ([]*Column, error)
	GetIndexes(ctx context.Context, schema string) ([]*Index, error)
	GetForeignKeys(ctx context.Context, schema string) ([]*ForeignKey, error)

	// Stored procedure operations
	GetStoredProcs(ctx context.Context, schema string) ([]*StoredProc, error)
	GetProcParams(ctx context.Context, schema string) ([]*ProcParam, error)

	// Trigger operations. Dialects without trigger support return (nil, nil);
	// only absence-of-feature is signalled by the nil slice, errors are real
	// introspection failures.
	GetTriggers(ctx context.Context, schema string) ([]*Trigger, error)
}

// NewInspector creates an inspector for the given database driver
func NewInspector(db *sql.DB, driver string, bounds *Bounds) (Inspector, error) {
	reader := &boundedDB{DB: db, driver: driver, bounds: bounds}
	switch driver {
	case "sqlserver":
		return &SQLServerInspector{db: reader}, nil
	case "postgres":
		return &PostgresInspector{db: reader}, nil
	default:
		return nil, fmt.Errorf("unsupported database driver: %s", driver)
	}
}
