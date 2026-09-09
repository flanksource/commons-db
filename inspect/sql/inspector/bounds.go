package inspector

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Bounds is owned by one inspection. Rows caps each query; DefinitionBytes is
// consumed across queries and schemas. Oversized definitions are omitted rather
// than exposing incomplete SQL as a usable object definition.
type Bounds struct {
	Rows            int
	DefinitionBytes int
	Truncated       bool
	definitionFull  bool
}

type boundedDB struct {
	*sql.DB
	driver string
	bounds *Bounds
}

// QueryContext caps the database result before the driver buffers rows, with one
// lookahead row to distinguish an exact fit from a truncated catalog.
func (db *boundedDB) QueryContext(ctx context.Context, query string, args ...any) (*boundedRows, error) {
	limit := max(db.bounds.Rows, 0)
	if db.bounds.definitionFull {
		limit = 0
	}
	query = strings.TrimSpace(query)
	if db.driver == "sqlserver" {
		query = strings.Replace(query, "SELECT", fmt.Sprintf("SELECT TOP (@p%d)", len(args)+1), 1)
	} else {
		query += fmt.Sprintf(" LIMIT $%d", len(args)+1)
	}
	rows, err := db.DB.QueryContext(ctx, query, append(args, limit+1)...)
	if err != nil {
		return nil, err
	}
	return &boundedRows{Rows: rows, remaining: limit, bounds: db.bounds}, nil
}

func (db *boundedDB) definitionLimit() int {
	return max(db.bounds.DefinitionBytes, 0) + 1
}

func (db *boundedDB) definition(value string) string {
	if len(value) > db.bounds.DefinitionBytes {
		db.bounds.Truncated = true
		db.bounds.definitionFull = true
		db.bounds.DefinitionBytes = 0
		return ""
	}
	db.bounds.DefinitionBytes -= len(value)
	return value
}

type boundedRows struct {
	*sql.Rows
	remaining int
	bounds    *Bounds
}

func (rows *boundedRows) Next() bool {
	if rows.bounds.definitionFull {
		return false
	}
	if !rows.Rows.Next() {
		return false
	}
	if rows.remaining == 0 {
		rows.bounds.Truncated = true
		return false
	}
	rows.remaining--
	return true
}
