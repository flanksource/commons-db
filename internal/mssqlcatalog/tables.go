package mssqlcatalog

import (
	"context"
	"database/sql"
	"fmt"
)

// Schemas lists non-system schemas, optionally restricted to names.
func Schemas(ctx context.Context, q Queryer, names []string) ([]string, error) {
	if err := validateNames(names); err != nil {
		return nil, err
	}
	var b binds
	query := fmt.Sprintf(`SELECT SCHEMA_NAME
		FROM INFORMATION_SCHEMA.SCHEMATA
		WHERE SCHEMA_NAME NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')
		AND SCHEMA_NAME NOT LIKE 'db[_]%%'%s
		ORDER BY SCHEMA_NAME`, b.in("SCHEMA_NAME", names))
	return collect(ctx, q, query, b.args, func(rows Rows) (string, error) {
		var schema string
		return schema, rows.Scan(&schema)
	})
}

// TableType selects base tables, views, or (for any other value) both,
// matching the inspector's "table" / "view" vocabulary.
type TableType string

const (
	BaseTables TableType = "table"
	Views      TableType = "view"
)

// Table is one INFORMATION_SCHEMA.TABLES row. Type is the catalog's TABLE_TYPE
// ("BASE TABLE" or "VIEW"); Definition is the view body, empty for tables and
// nil when the catalog withholds it.
type Table struct {
	Schema     string
	Name       string
	Type       string
	Definition *string
}

// Tables lists the tables and/or views of a schema ordered by name.
func Tables(ctx context.Context, q Queryer, scope Scope, tableType TableType) ([]Table, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	schema, kind := b.add(scope.Schema), b.add(string(tableType))
	query := fmt.Sprintf(`SELECT
			t.TABLE_SCHEMA,
			t.TABLE_NAME,
			t.TABLE_TYPE,
			CASE WHEN t.TABLE_TYPE = 'VIEW' THEN %s ELSE '' END AS VIEW_DEF
		FROM INFORMATION_SCHEMA.TABLES t
		LEFT JOIN sys.objects o
		  ON o.object_id = OBJECT_ID(t.TABLE_SCHEMA + '.' + t.TABLE_NAME)
		WHERE t.TABLE_SCHEMA = %s
		AND t.TABLE_TYPE = CASE %s
			WHEN 'table' THEN 'BASE TABLE'
			WHEN 'view' THEN 'VIEW'
			ELSE t.TABLE_TYPE
		END%s
		ORDER BY t.TABLE_NAME`,
		b.definition("OBJECT_DEFINITION(o.object_id)", scope.DefinitionLimit), schema, kind, b.in("t.TABLE_NAME", scope.Names))
	return collect(ctx, q, query, b.args, func(rows Rows) (Table, error) {
		var t Table
		var definition sql.NullString
		err := rows.Scan(&t.Schema, &t.Name, &t.Type, &definition)
		t.Definition = nullableString(definition)
		return t, err
	})
}

// Column is one table or view column.
//
// CharMaxLength, NumericPrecision and NumericScale come from INFORMATION_SCHEMA
// (character widths, -1 for max; nil for types without one). MaxLength,
// Precision and Scale are the raw sys.columns values (bytes, so nvarchar is
// doubled; -1 for max) that DDL generation needs; they are nil only when the
// sys.columns join misses. Default is the default expression and
// DefaultConstraint the name of the constraint holding it. IdentitySeed and
// IdentityIncrement are set for identity columns. Computed and
// UserDefinedType flag columns DDL generation cannot reproduce.
type Column struct {
	Table             string
	Name              string
	DataType          string
	Nullable          bool
	Default           *string
	DefaultConstraint *string
	Position          int
	CharMaxLength     *int
	NumericPrecision  *int
	NumericScale      *int
	MaxLength         *int
	Precision         *int
	Scale             *int
	PrimaryKey        bool
	Unique            bool
	Identity          bool
	IdentitySeed      *int64
	IdentityIncrement *int64
	Computed          bool
	UserDefinedType   bool
}

// Columns lists every column of every table and view in the scope, ordered by
// table then ordinal position. sys.columns is joined only by name for
// identity, raw widths and the default constraint, which avoids a per-row
// COLUMNPROPERTY(OBJECT_ID(...)) call at schema scope.
func Columns(ctx context.Context, q Queryer, scope Scope) ([]Column, error) {
	if err := scope.validate(); err != nil {
		return nil, err
	}
	var b binds
	schema := b.add(scope.Schema)
	query := fmt.Sprintf(`SELECT
			c.TABLE_NAME,
			c.COLUMN_NAME,
			c.DATA_TYPE,
			CASE WHEN c.IS_NULLABLE = 'YES' THEN 1 ELSE 0 END,
			c.COLUMN_DEFAULT,
			c.ORDINAL_POSITION,
			c.CHARACTER_MAXIMUM_LENGTH,
			c.NUMERIC_PRECISION,
			c.NUMERIC_SCALE,
			CASE WHEN pk.COLUMN_NAME IS NULL THEN 0 ELSE 1 END,
			CASE WHEN uq.COLUMN_NAME IS NULL THEN 0 ELSE 1 END,
			sc.is_identity,
			sc.max_length,
			sc.precision,
			sc.scale,
			dc.name,
			CAST(idc.seed_value AS bigint),
			CAST(idc.increment_value AS bigint),
			sc.is_computed,
			ty.is_user_defined
		FROM INFORMATION_SCHEMA.COLUMNS c
		LEFT JOIN sys.schemas ss ON ss.name = c.TABLE_SCHEMA
		LEFT JOIN sys.objects so ON so.schema_id = ss.schema_id AND so.name = c.TABLE_NAME
		LEFT JOIN sys.columns sc ON sc.object_id = so.object_id AND sc.name = c.COLUMN_NAME
		LEFT JOIN sys.types ty ON ty.user_type_id = sc.user_type_id
		LEFT JOIN sys.identity_columns idc ON idc.object_id = sc.object_id AND idc.column_id = sc.column_id
		LEFT JOIN sys.default_constraints dc ON dc.parent_object_id = sc.object_id AND dc.parent_column_id = sc.column_id
		LEFT JOIN (%s) pk
		  ON pk.TABLE_SCHEMA = c.TABLE_SCHEMA AND pk.TABLE_NAME = c.TABLE_NAME AND pk.COLUMN_NAME = c.COLUMN_NAME
		LEFT JOIN (%s) uq
		  ON uq.TABLE_SCHEMA = c.TABLE_SCHEMA AND uq.TABLE_NAME = c.TABLE_NAME AND uq.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.TABLE_SCHEMA = %s%s
		ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION`,
		keyColumns("SELECT", "PRIMARY KEY", schema), keyColumns("SELECT DISTINCT", "UNIQUE", schema),
		schema, b.in("c.TABLE_NAME", scope.Names))
	return collect(ctx, q, query, b.args, scanColumn)
}

func keyColumns(selectClause, constraintType, schema string) string {
	return fmt.Sprintf(`%s ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
			FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
			JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
			  ON ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			 AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
			 AND ku.TABLE_NAME = tc.TABLE_NAME
			WHERE tc.CONSTRAINT_TYPE = '%s' AND tc.TABLE_SCHEMA = %s`, selectClause, constraintType, schema)
}

func scanColumn(rows Rows) (Column, error) {
	var c Column
	var nullable, primaryKey, unique int
	var defaultValue, defaultName sql.NullString
	var charMax, numPrecision, numScale, maxLength, precision, scale sql.NullInt64
	// is_identity is `bit`: go-mssqldb decodes it to a Go bool, and scanning
	// a bool into an int fails. NULL when the sys.objects join misses.
	var identity, computed, userDefined sql.NullBool
	var seed, increment sql.NullInt64
	if err := rows.Scan(&c.Table, &c.Name, &c.DataType, &nullable, &defaultValue, &c.Position,
		&charMax, &numPrecision, &numScale, &primaryKey, &unique, &identity,
		&maxLength, &precision, &scale, &defaultName, &seed, &increment, &computed, &userDefined); err != nil {
		return c, err
	}
	c.Nullable, c.PrimaryKey, c.Unique = nullable == 1, primaryKey == 1, unique == 1
	c.Identity = identity.Valid && identity.Bool
	c.Computed, c.UserDefinedType = computed.Valid && computed.Bool, userDefined.Valid && userDefined.Bool
	if seed.Valid && increment.Valid {
		c.IdentitySeed, c.IdentityIncrement = new(seed.Int64), new(increment.Int64)
	}
	c.Default, c.DefaultConstraint = nullableString(defaultValue), nullableString(defaultName)
	c.CharMaxLength, c.NumericPrecision, c.NumericScale = nullableInt(charMax), nullableInt(numPrecision), nullableInt(numScale)
	c.MaxLength, c.Precision, c.Scale = nullableInt(maxLength), nullableInt(precision), nullableInt(scale)
	return c, nil
}
