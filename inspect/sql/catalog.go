// Package sqlinspect provides reusable, read-only SQL catalog inspection.
package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"
	"sort"
	"strings"
)

const (
	DefaultMaxRelations = 5000
	DefaultMaxColumns   = 50000
)

type Limits struct {
	MaxRelations int
	MaxColumns   int
}

func (l Limits) withDefaults() Limits {
	if l.MaxRelations <= 0 {
		l.MaxRelations = DefaultMaxRelations
	}
	if l.MaxColumns <= 0 {
		l.MaxColumns = DefaultMaxColumns
	}
	return l
}

type Catalog struct {
	Driver         string   `json:"driver"`
	Database       string   `json:"database,omitempty"`
	Databases      []string `json:"databases,omitempty"`
	DefaultSchema  string   `json:"defaultSchema,omitempty"`
	Schemas        []Schema `json:"schemas"`
	Truncated      bool     `json:"truncated,omitempty"`
	TruncateReason string   `json:"truncateReason,omitempty"`
}

type Schema struct {
	Name      string     `json:"name"`
	Relations []Relation `json:"relations"`
}

type Relation struct {
	Name    string   `json:"name"`
	Type    string   `json:"type"`
	Columns []Column `json:"columns"`
}

type Column struct {
	Name     string `json:"name"`
	DataType string `json:"dataType,omitempty"`
	Ordinal  int    `json:"ordinal,omitempty"`
}

type columnRow struct {
	schema, relation, relationType, column, dataType string
	ordinal                                          int
}

// Inspect returns the current database's schemas, relations and columns using
// set-based catalog queries. It deliberately does not inspect other databases
// on the same server. Schemas are queried independently so empty schemas remain
// visible to callers.
func Inspect(ctx context.Context, db *sql.DB, driver string, limits Limits) (Catalog, error) {
	if db == nil {
		return Catalog{}, fmt.Errorf("nil sql database")
	}
	driver = normalizeDriver(driver)
	identity, statement, err := inspectionQueries(driver)
	if err != nil {
		return Catalog{}, err
	}

	var database, defaultSchema string
	if err := db.QueryRowContext(ctx, identity).Scan(&database, &defaultSchema); err != nil {
		return Catalog{}, fmt.Errorf("inspect sql identity: %w", err)
	}
	databases, err := ListDatabases(ctx, db, driver)
	if err != nil {
		return Catalog{}, err
	}
	schemaRows, err := db.QueryContext(ctx, schemaInspectionQuery(driver))
	if err != nil {
		return Catalog{}, fmt.Errorf("inspect sql schemas: %w", err)
	}
	var schemas []string
	for schemaRows.Next() {
		var schema string
		if err := schemaRows.Scan(&schema); err != nil {
			schemaRows.Close()
			return Catalog{}, fmt.Errorf("scan sql schema: %w", err)
		}
		schemas = append(schemas, schema)
	}
	if err := schemaRows.Err(); err != nil {
		schemaRows.Close()
		return Catalog{}, fmt.Errorf("iterate sql schemas: %w", err)
	}
	schemaRows.Close()
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return Catalog{}, fmt.Errorf("inspect sql catalog: %w", err)
	}
	defer rows.Close()

	items := make([]columnRow, 0)
	for rows.Next() {
		var item columnRow
		if err := rows.Scan(&item.schema, &item.relation, &item.relationType, &item.column, &item.dataType, &item.ordinal); err != nil {
			return Catalog{}, fmt.Errorf("scan sql catalog: %w", err)
		}
		items = append(items, item)
	}
	if err := rows.Err(); err != nil {
		return Catalog{}, fmt.Errorf("iterate sql catalog: %w", err)
	}
	return buildCatalog(driver, database, defaultSchema, databases, schemas, items, limits), nil
}

// ListDatabases returns databases accessible to the current connection user.
func ListDatabases(ctx context.Context, db *sql.DB, driver string) ([]string, error) {
	if db == nil {
		return nil, fmt.Errorf("nil sql database")
	}
	statement := databaseInspectionQuery(driver)
	if statement == "" {
		return nil, fmt.Errorf("unsupported sql inspection driver %q", driver)
	}
	rows, err := db.QueryContext(ctx, statement)
	if err != nil {
		return nil, fmt.Errorf("inspect sql databases: %w", err)
	}
	defer rows.Close()
	var databases []string
	for rows.Next() {
		var database string
		if err := rows.Scan(&database); err != nil {
			return nil, fmt.Errorf("scan sql database: %w", err)
		}
		databases = append(databases, database)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sql databases: %w", err)
	}
	return databases, nil
}

func normalizeDriver(driver string) string {
	switch strings.ToLower(driver) {
	case "postgres", "postgresql", "pgx":
		return "postgres"
	case "mysql":
		return "mysql"
	case "sql_server", "sqlserver", "mssql":
		return "sqlserver"
	case "clickhouse":
		return "clickhouse"
	default:
		return strings.ToLower(driver)
	}
}

func inspectionQueries(driver string) (identity, catalog string, err error) {
	switch normalizeDriver(driver) {
	case "postgres":
		return `SELECT current_database(), current_schema()`, `
SELECT c.table_schema, c.table_name, t.table_type, c.column_name,
       COALESCE(c.udt_name, c.data_type), c.ordinal_position
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema NOT IN ('information_schema','pg_catalog')
  AND c.table_schema NOT LIKE 'pg_toast%'
ORDER BY c.table_schema, c.table_name, c.ordinal_position`, nil
	case "mysql":
		return `SELECT DATABASE(), DATABASE()`, `
SELECT c.table_schema, c.table_name, t.table_type, c.column_name,
       c.column_type, c.ordinal_position
FROM information_schema.columns c
JOIN information_schema.tables t
  ON t.table_schema = c.table_schema AND t.table_name = c.table_name
WHERE c.table_schema = DATABASE()
ORDER BY c.table_schema, c.table_name, c.ordinal_position`, nil
	case "sqlserver":
		return `SELECT DB_NAME(), SCHEMA_NAME()`, `
SELECT c.TABLE_SCHEMA, c.TABLE_NAME, t.TABLE_TYPE, c.COLUMN_NAME,
       c.DATA_TYPE, c.ORDINAL_POSITION
FROM INFORMATION_SCHEMA.COLUMNS c
JOIN INFORMATION_SCHEMA.TABLES t
  ON t.TABLE_SCHEMA = c.TABLE_SCHEMA AND t.TABLE_NAME = c.TABLE_NAME
WHERE c.TABLE_SCHEMA NOT IN ('INFORMATION_SCHEMA','sys')
ORDER BY c.TABLE_SCHEMA, c.TABLE_NAME, c.ORDINAL_POSITION`, nil
	case "clickhouse":
		return `SELECT currentDatabase(), currentDatabase()`, `
SELECT database, table, 'BASE TABLE', name, type, position
FROM system.columns
WHERE database NOT IN ('system','information_schema','INFORMATION_SCHEMA')
ORDER BY database, table, position`, nil
	default:
		return "", "", fmt.Errorf("unsupported sql inspection driver %q", driver)
	}
}

func schemaInspectionQuery(driver string) string {
	switch normalizeDriver(driver) {
	case "postgres":
		return `SELECT schema_name
FROM information_schema.schemata
WHERE schema_name NOT IN ('pg_catalog','information_schema','pg_toast')
  AND schema_name NOT LIKE 'pg_temp_%'
  AND schema_name NOT LIKE 'pg_toast_temp_%'
ORDER BY schema_name`
	case "mysql":
		return `SELECT schema_name
FROM information_schema.schemata
WHERE schema_name = DATABASE()
ORDER BY schema_name`
	case "sqlserver":
		return `SELECT schema_name
FROM information_schema.schemata
WHERE schema_name NOT IN ('sys','INFORMATION_SCHEMA','guest')
  AND schema_name NOT LIKE 'db_%'
ORDER BY schema_name`
	case "clickhouse":
		return `SELECT name FROM system.databases WHERE name = currentDatabase() ORDER BY name`
	default:
		return ""
	}
}

func databaseInspectionQuery(driver string) string {
	switch normalizeDriver(driver) {
	case "postgres":
		return `SELECT datname
FROM pg_database
WHERE datallowconn AND NOT datistemplate AND has_database_privilege(datname, 'CONNECT')
ORDER BY datname`
	case "mysql":
		return `SELECT schema_name
FROM information_schema.schemata
WHERE schema_name NOT IN ('information_schema','mysql','performance_schema','sys')
ORDER BY schema_name`
	case "sqlserver":
		return `SELECT name FROM sys.databases WHERE state = 0 AND HAS_DBACCESS(name) = 1 ORDER BY name`
	case "clickhouse":
		return `SELECT name
FROM system.databases
WHERE name NOT IN ('system','information_schema','INFORMATION_SCHEMA')
ORDER BY name`
	default:
		return ""
	}
}

func buildCatalog(driver, database, defaultSchema string, databases, schemas []string, rows []columnRow, limits Limits) Catalog {
	limits = limits.withDefaults()
	type relationKey struct{ schema, relation string }
	relations := make(map[relationKey]*Relation)
	schemaNames := make(map[string]struct{})
	for _, schema := range schemas {
		if schema != "" {
			schemaNames[schema] = struct{}{}
		}
	}
	if defaultSchema != "" {
		schemaNames[defaultSchema] = struct{}{}
	}
	relationCount, columnCount := 0, 0
	truncated, reason := false, ""

	for _, row := range rows {
		key := relationKey{row.schema, row.relation}
		relation := relations[key]
		if relation == nil {
			if relationCount >= limits.MaxRelations {
				truncated, reason = true, fmt.Sprintf("relation limit %d reached", limits.MaxRelations)
				continue
			}
			relation = &Relation{Name: row.relation, Type: normalizeRelationType(row.relationType), Columns: []Column{}}
			relations[key] = relation
			schemaNames[row.schema] = struct{}{}
			relationCount++
		}
		if columnCount >= limits.MaxColumns {
			truncated, reason = true, fmt.Sprintf("column limit %d reached", limits.MaxColumns)
			continue
		}
		relation.Columns = append(relation.Columns, Column{Name: row.column, DataType: row.dataType, Ordinal: row.ordinal})
		columnCount++
	}

	names := make([]string, 0, len(schemaNames))
	for name := range schemaNames {
		names = append(names, name)
	}
	sort.Strings(names)
	databaseNames := make(map[string]struct{}, len(databases)+1)
	for _, name := range databases {
		if name != "" {
			databaseNames[name] = struct{}{}
		}
	}
	if database != "" {
		databaseNames[database] = struct{}{}
	}
	orderedDatabases := make([]string, 0, len(databaseNames))
	for name := range databaseNames {
		orderedDatabases = append(orderedDatabases, name)
	}
	sort.Strings(orderedDatabases)
	catalog := Catalog{Driver: normalizeDriver(driver), Database: database, Databases: orderedDatabases, DefaultSchema: defaultSchema, Schemas: []Schema{}, Truncated: truncated, TruncateReason: reason}
	for _, schemaName := range names {
		relationNames := make([]string, 0)
		for key := range relations {
			if key.schema == schemaName {
				relationNames = append(relationNames, key.relation)
			}
		}
		sort.Strings(relationNames)
		schema := Schema{Name: schemaName, Relations: make([]Relation, 0, len(relationNames))}
		for _, relationName := range relationNames {
			schema.Relations = append(schema.Relations, *relations[relationKey{schemaName, relationName}])
		}
		catalog.Schemas = append(catalog.Schemas, schema)
	}
	return catalog
}

func normalizeRelationType(value string) string {
	if strings.Contains(strings.ToLower(value), "view") {
		return "view"
	}
	return "table"
}
