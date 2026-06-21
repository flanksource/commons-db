// Package processor contains built-in post-query processors for the query
// engine: sqlite-backed merge and key-based reconciliation. Each processor
// self-registers via init(); consumers enable them with a blank import:
//
//	import _ "github.com/flanksource/commons-db/query/processor"
package processor

import (
	"encoding/json"
	"fmt"
	"maps"
	"reflect"
	"sort"
	"strings"
	"time"

	"github.com/glebarez/sqlite"
	"gorm.io/gorm"

	"github.com/flanksource/commons-db/context"
	"github.com/flanksource/commons-db/db"
	"github.com/flanksource/commons-db/query"
)

// SQLite storage classes used for inferred column types.
const (
	sqliteText    = "TEXT"
	sqliteInteger = "INTEGER"
	sqliteReal    = "REAL"
	sqliteBlob    = "BLOB"
)

// sampleSize is the number of rows sampled to infer column types.
const sampleSize = 150

// ResultSet pairs a table name with the rows to load into the merge database.
type ResultSet struct {
	Name string
	Rows []query.Row
}

// Merge loads each ResultSet into an in-memory SQLite database as a table, then
// runs mergeSQL (an arbitrary join/aggregation across those tables) and returns
// the resulting rows. Ported from duty/dataquery/sqlite.go.
func Merge(ctx context.Context, mergeSQL string, sets ...ResultSet) ([]query.Row, error) {
	if mergeSQL == "" {
		return nil, fmt.Errorf("merge sql is required")
	}
	if len(sets) == 0 {
		return nil, fmt.Errorf("merge requires at least one result set")
	}

	gormDB, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	if err != nil {
		return nil, fmt.Errorf("failed to open in-memory sqlite: %w", err)
	}
	sqlDB, err := gormDB.DB()
	if err != nil {
		return nil, fmt.Errorf("failed to get sql.DB: %w", err)
	}
	defer func() { _ = sqlDB.Close() }()

	for _, set := range sets {
		if err := createTable(gormDB, set); err != nil {
			return nil, fmt.Errorf("failed to create table %q: %w", set.Name, err)
		}
		if err := insertRows(gormDB, set); err != nil {
			return nil, fmt.Errorf("failed to insert into %q: %w", set.Name, err)
		}
	}

	rows, err := sqlDB.QueryContext(ctx, mergeSQL)
	if err != nil {
		return nil, fmt.Errorf("failed to run merge query: %w", err)
	}
	defer func() { _ = rows.Close() }()

	return db.ScanRows[query.Row](rows)
}

func createTable(gormDB *gorm.DB, set ResultSet) error {
	if len(set.Rows) == 0 {
		return fmt.Errorf("cannot infer schema from an empty result set")
	}

	types := inferColumnTypes(set.Rows)

	names := make([]string, 0, len(types))
	for name := range types {
		names = append(names, name)
	}
	sort.Strings(names)

	defs := make([]string, 0, len(names))
	for _, name := range names {
		defs = append(defs, fmt.Sprintf(`"%s" %s`, name, types[name]))
	}

	ddl := fmt.Sprintf(`CREATE TABLE "%s" (%s)`, set.Name, strings.Join(defs, ", "))
	return gormDB.Exec(ddl).Error
}

func insertRows(gormDB *gorm.DB, set ResultSet) error {
	toInsert := make([]map[string]any, 0, len(set.Rows))
	for _, row := range set.Rows {
		clone := map[string]any(maps.Clone(row))
		if err := normalizeRow(clone); err != nil {
			return err
		}
		toInsert = append(toInsert, clone)
	}

	return gormDB.Table(set.Name).CreateInBatches(toInsert, 100).Error
}

// inferColumnTypes samples rows to pick the widest compatible SQLite type per column.
func inferColumnTypes(rows []query.Row) map[string]string {
	seen := map[string]map[string]bool{}
	for i, row := range rows {
		if i >= sampleSize {
			break
		}
		for col, val := range row {
			if seen[col] == nil {
				seen[col] = map[string]bool{}
			}
			if val != nil {
				seen[col][goTypeToSQLite(val)] = true
			}
		}
	}

	out := make(map[string]string, len(seen))
	for col, types := range seen {
		out[col] = widestType(types)
	}
	return out
}

func widestType(types map[string]bool) string {
	switch {
	case len(types) == 0:
		return sqliteText
	case types[sqliteBlob]:
		return sqliteBlob
	case types[sqliteText]:
		return sqliteText
	case types[sqliteReal]:
		return sqliteReal
	default:
		return sqliteInteger
	}
}

func goTypeToSQLite(value any) string {
	switch value.(type) {
	case int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64:
		return sqliteInteger
	case float32, float64:
		return sqliteReal
	case bool:
		return sqliteInteger
	case time.Time:
		return sqliteText
	case string:
		return sqliteText
	case []byte, json.RawMessage:
		return sqliteBlob
	default:
		rv := reflect.ValueOf(value)
		switch rv.Kind() {
		case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
			reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
			return sqliteInteger
		case reflect.Float32, reflect.Float64:
			return sqliteReal
		case reflect.Bool:
			return sqliteInteger
		case reflect.Map, reflect.Slice:
			return sqliteBlob
		default:
			return sqliteText
		}
	}
}

// normalizeRow JSON-encodes complex values (maps/slices/structs) so they store
// cleanly as SQLite BLOB/TEXT.
func normalizeRow(row map[string]any) error {
	for k, v := range row {
		nv, err := normalizeValue(v)
		if err != nil {
			return fmt.Errorf("column %q: %w", k, err)
		}
		row[k] = nv
	}
	return nil
}

func normalizeValue(v any) (any, error) {
	switch x := v.(type) {
	case nil, bool, int, int8, int16, int32, int64, uint, uint8, uint16, uint32, uint64,
		float32, float64, string, []byte, time.Time, *time.Time:
		return x, nil
	case json.RawMessage:
		return []byte(x), nil
	default:
		b, err := json.Marshal(x)
		if err != nil {
			return nil, err
		}
		return b, nil
	}
}
