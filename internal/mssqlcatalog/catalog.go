// Package mssqlcatalog reads SQL Server catalog views into plain row structs.
//
// It is the single owner of the catalog SQL shared by the schema inspector and
// the Atlas SQL Server driver. Every query is schema-scoped, runs in one round
// trip, pushes name filters into SQL as ordinary binds, and returns rows in a
// deterministic owner-then-natural order.
//
// Queries must run on databases at compatibility level 100 (OIPA): no
// STRING_AGG, no OPENJSON. Ordered string aggregation uses FOR XML PATH, and
// name filters are plain `IN (@p1, @p2, ...)` lists. Each query text starts
// with its outer SELECT so a caller may prefix it with a TOP bound.
package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
	"strings"
)

// Rows is the subset of *sql.Rows the catalog reads.
type Rows interface {
	Next() bool
	Scan(dest ...any) error
	Err() error
	Close() error
}

// Queryer runs a catalog query. Callers that bound or instrument queries
// implement it; FromDB adapts database/sql handles.
type Queryer interface {
	QueryContext(ctx context.Context, query string, args ...any) (Rows, error)
}

// FromDB adapts db to Queryer.
func FromDB(db SQLQueryer) Queryer { return sqlDB{db} }

type SQLQueryer interface {
	QueryContext(context.Context, string, ...any) (*sql.Rows, error)
}

type sqlDB struct{ db SQLQueryer }

func (d sqlDB) QueryContext(ctx context.Context, query string, args ...any) (Rows, error) {
	rows, err := d.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

// Scope selects the catalog rows a query returns.
type Scope struct {
	// Schema is the owning schema; required.
	Schema string
	// Names restricts rows to these owning objects (tables for table-level
	// queries, routines for Routines and Parameters). Empty means all.
	Names []string
	// DefinitionLimit truncates object definitions server-side to this many
	// characters. Zero returns definitions in full.
	DefinitionLimit int
}

func (s Scope) validate() error {
	if strings.TrimSpace(s.Schema) == "" {
		return fmt.Errorf("SQL Server catalog query: schema is required")
	}
	if s.DefinitionLimit < 0 {
		return fmt.Errorf("SQL Server catalog query: definition limit must not be negative, got %d", s.DefinitionLimit)
	}
	return validateNames(s.Names)
}

func validateNames(names []string) error {
	for i, name := range names {
		if strings.TrimSpace(name) == "" {
			return fmt.Errorf("SQL Server catalog query: name filter entry %d is empty", i)
		}
	}
	return nil
}

// binds numbers SQL Server @pN placeholders in the order values are added.
type binds struct{ args []any }

func (b *binds) add(value any) string {
	b.args = append(b.args, value)
	return fmt.Sprintf("@p%d", len(b.args))
}

// in renders ` AND <column> IN (@pX, ...)`, or nothing for an empty filter.
func (b *binds) in(column string, names []string) string {
	if len(names) == 0 {
		return ""
	}
	placeholders := make([]string, len(names))
	for i, name := range names {
		placeholders[i] = b.add(name)
	}
	return fmt.Sprintf(" AND %s IN (%s)", column, strings.Join(placeholders, ", "))
}

// definition wraps expr in LEFT(expr, @pN) when limit is set.
func (b *binds) definition(expr string, limit int) string {
	if limit == 0 {
		return expr
	}
	return fmt.Sprintf("LEFT(%s, %s)", expr, b.add(limit))
}

func collect[T any](ctx context.Context, q Queryer, query string, args []any, scan func(Rows) (T, error)) (out []T, err error) {
	rows, err := q.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() {
		if closeErr := rows.Close(); err == nil {
			err = closeErr
		}
	}()
	for rows.Next() {
		row, err := scan(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	return new(int(v.Int64))
}

func nullableString(v sql.NullString) *string {
	if !v.Valid {
		return nil
	}
	return new(v.String)
}
