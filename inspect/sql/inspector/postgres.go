package inspector

import (
	"context"
	"database/sql"
	"strings"
)

// PostgresInspector provides PostgreSQL database introspection
type PostgresInspector struct {
	db *sql.DB
}

// GetSchemas returns all non-system schemas in the database
func (i *PostgresInspector) GetSchemas(ctx context.Context) ([]string, error) {
	query := `
		SELECT schema_name
		FROM information_schema.schemata
		WHERE schema_name NOT IN ('pg_catalog', 'information_schema', 'pg_toast')
		AND schema_name NOT LIKE 'pg_temp_%'
		AND schema_name NOT LIKE 'pg_toast_temp_%'
		ORDER BY schema_name
	`

	rows, err := i.db.QueryContext(ctx, query)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var schemas []string
	for rows.Next() {
		var schema string
		if err := rows.Scan(&schema); err != nil {
			return nil, err
		}
		schemas = append(schemas, schema)
	}
	return schemas, rows.Err()
}

// GetDefaultSchema returns the default schema for the current user
func (i *PostgresInspector) GetDefaultSchema(ctx context.Context) (string, error) {
	var schema string
	err := i.db.QueryRowContext(ctx, "SELECT current_schema()").Scan(&schema)
	return schema, err
}

// GetDatabaseName returns the current database name
func (i *PostgresInspector) GetDatabaseName(ctx context.Context) (string, error) {
	var dbName string
	err := i.db.QueryRowContext(ctx, "SELECT current_database()").Scan(&dbName)
	return dbName, err
}

// GetTables returns all tables or views in the specified schema
func (i *PostgresInspector) GetTables(ctx context.Context, schema string, tableType string) ([]*Table, error) {
	pgType := "BASE TABLE"
	if tableType == "view" {
		pgType = "VIEW"
	}

	query := `
		SELECT
			table_schema,
			table_name,
			table_type,
			COALESCE(view_definition, '') as view_def
		FROM information_schema.tables
		LEFT JOIN information_schema.views USING (table_schema, table_name)
		WHERE table_schema = $1
		AND table_type = $2
		ORDER BY table_name
	`

	rows, err := i.db.QueryContext(ctx, query, schema, pgType)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []*Table
	for rows.Next() {
		var t Table
		var viewDef sql.NullString
		if err := rows.Scan(&t.Schema, &t.Name, &t.Type, &viewDef); err != nil {
			return nil, err
		}
		if viewDef.Valid {
			t.ViewDef = viewDef.String
		}
		tables = append(tables, &t)
	}
	return tables, rows.Err()
}

// GetColumns returns every column of every table and view in the schema
func (i *PostgresInspector) GetColumns(ctx context.Context, schema string) ([]*Column, error) {
	query := `
		SELECT
			table_name,
			column_name,
			COALESCE(NULLIF(data_type, 'USER-DEFINED'), udt_name),
			CASE WHEN is_nullable = 'YES' THEN true ELSE false END,
			column_default,
			ordinal_position,
			CASE WHEN is_identity = 'YES' THEN true ELSE false END,
			character_maximum_length,
			numeric_precision,
			numeric_scale,
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu USING (constraint_catalog, constraint_schema, constraint_name)
				WHERE tc.constraint_type = 'PRIMARY KEY' AND tc.table_schema = columns.table_schema
				  AND tc.table_name = columns.table_name AND kcu.column_name = columns.column_name
			),
			EXISTS (
				SELECT 1 FROM information_schema.table_constraints tc
				JOIN information_schema.key_column_usage kcu USING (constraint_catalog, constraint_schema, constraint_name)
				WHERE tc.constraint_type = 'UNIQUE' AND tc.table_schema = columns.table_schema
				  AND tc.table_name = columns.table_name AND kcu.column_name = columns.column_name
			)
		FROM information_schema.columns
		WHERE table_schema = $1
		ORDER BY table_name, ordinal_position
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []*Column
	for rows.Next() {
		var c Column
		var defaultVal sql.NullString
		var maxLength, precision, scale sql.NullInt64
		if err := rows.Scan(&c.TableName, &c.ColumnName, &c.DataType, &c.IsNullable, &defaultVal, &c.Position,
			&c.IsAutoIncrement, &maxLength, &precision, &scale, &c.IsPrimaryKey, &c.IsUnique); err != nil {
			return nil, err
		}
		if defaultVal.Valid {
			c.DefaultValue = &defaultVal.String
		}
		c.MaxLength = nullableInt(maxLength)
		c.NumericPrecision = nullableInt(precision)
		c.NumericScale = nullableInt(scale)
		columns = append(columns, &c)
	}
	return columns, rows.Err()
}

// GetIndexes returns every index on every relation in the schema.
func (i *PostgresInspector) GetIndexes(ctx context.Context, schema string) ([]*Index, error) {
	query := `
		SELECT
			t.relname as table_name,
			i.relname as index_name,
			ix.indisunique as is_unique,
			ix.indisprimary as is_primary,
			array_agg(a.attname ORDER BY keys.ordinality) as columns,
			am.amname,
			COALESCE(pg_get_expr(ix.indpred, ix.indrelid), '')
		FROM pg_class t
		INNER JOIN pg_namespace n ON t.relnamespace = n.oid
		INNER JOIN pg_index ix ON t.oid = ix.indrelid
		INNER JOIN pg_class i ON i.oid = ix.indexrelid
		INNER JOIN pg_am am ON i.relam = am.oid
		CROSS JOIN LATERAL unnest(ix.indkey) WITH ORDINALITY AS keys(attnum, ordinality)
		INNER JOIN pg_attribute a ON a.attrelid = t.oid AND a.attnum = keys.attnum
		WHERE n.nspname = $1
		GROUP BY t.relname, i.relname, ix.indisunique, ix.indisprimary, am.amname, ix.indpred, ix.indrelid
		ORDER BY t.relname, i.relname
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []*Index
	for rows.Next() {
		var idx Index
		var columnsArr string
		if err := rows.Scan(&idx.TableName, &idx.IndexName, &idx.IsUnique, &idx.IsPrimary, &columnsArr, &idx.Type, &idx.Condition); err != nil {
			return nil, err
		}
		// PostgreSQL returns array as {col1,col2,col3}
		columnsArr = strings.Trim(columnsArr, "{}")
		if columnsArr != "" {
			idx.Columns = strings.Split(columnsArr, ",")
		}
		indexes = append(indexes, &idx)
	}
	return indexes, rows.Err()
}

// GetForeignKeys returns every foreign key column in the schema.
func (i *PostgresInspector) GetForeignKeys(ctx context.Context, schema string) ([]*ForeignKey, error) {
	query := `
		SELECT
			src.relname, con.conname, src_col.attname, ref.relname, ref_col.attname
		FROM pg_constraint con
		JOIN pg_class src ON src.oid = con.conrelid
		JOIN pg_namespace ns ON ns.oid = src.relnamespace
		JOIN pg_class ref ON ref.oid = con.confrelid
		CROSS JOIN LATERAL unnest(con.conkey) WITH ORDINALITY src_key(attnum, ordinality)
		JOIN LATERAL unnest(con.confkey) WITH ORDINALITY ref_key(attnum, ordinality)
		  ON ref_key.ordinality = src_key.ordinality
		JOIN pg_attribute src_col ON src_col.attrelid = src.oid AND src_col.attnum = src_key.attnum
		JOIN pg_attribute ref_col ON ref_col.attrelid = ref.oid AND ref_col.attnum = ref_key.attnum
		WHERE con.contype = 'f' AND ns.nspname = $1
		ORDER BY src.relname, con.conname, src_key.ordinality
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fks []*ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.TableName, &fk.ConstraintName, &fk.ColumnName, &fk.RefTableName, &fk.RefColumnName); err != nil {
			return nil, err
		}
		fks = append(fks, &fk)
	}
	return fks, rows.Err()
}

// GetStoredProcs returns all stored procedures and functions in the specified schema
func (i *PostgresInspector) GetStoredProcs(ctx context.Context, schema string) ([]*StoredProc, error) {
	query := `
		SELECT
			n.nspname AS schema_name,
			p.proname AS proc_name,
			CASE p.prokind
				WHEN 'f' THEN 'function'
				WHEN 'p' THEN 'procedure'
				ELSE 'function'
			END AS proc_type,
			pg_get_functiondef(p.oid) AS proc_sql
		FROM pg_proc p
		INNER JOIN pg_namespace n ON p.pronamespace = n.oid
		WHERE n.nspname = $1
		AND p.prokind IN ('f', 'p')
		ORDER BY p.proname
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procs []*StoredProc
	for rows.Next() {
		var p StoredProc
		var procSQL sql.NullString
		if err := rows.Scan(&p.Schema, &p.Name, &p.Type, &procSQL); err != nil {
			return nil, err
		}
		if procSQL.Valid {
			p.SQL = procSQL.String
		}
		procs = append(procs, &p)
	}
	return procs, rows.Err()
}

// GetProcParams returns the parameters of every routine in the schema.
func (i *PostgresInspector) GetProcParams(ctx context.Context, schema string) ([]*ProcParam, error) {
	query := `
		SELECT
			pr.proname AS proc_name,
			p.parameter_name,
			p.data_type,
			p.ordinal_position
		FROM information_schema.parameters p
		INNER JOIN pg_proc pr ON p.specific_name = pr.proname || '_' || pr.oid
		INNER JOIN pg_namespace n ON pr.pronamespace = n.oid
		WHERE n.nspname = $1
		AND p.parameter_mode = 'IN'
		ORDER BY pr.proname, p.ordinal_position
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var params []*ProcParam
	for rows.Next() {
		var p ProcParam
		if err := rows.Scan(&p.ProcName, &p.ParamName, &p.DataType, &p.Position); err != nil {
			return nil, err
		}
		params = append(params, &p)
	}
	return params, rows.Err()
}

// GetTriggers returns triggers in the schema. Not yet implemented for
// Postgres — returns nil, nil so the extractor can degrade gracefully.
// TODO: triggers — MSSQL-only for now.
func (i *PostgresInspector) GetTriggers(ctx context.Context, schema string) ([]*Trigger, error) {
	return nil, nil
}
