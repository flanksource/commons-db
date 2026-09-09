package inspector

import (
	"context"
	"database/sql"
	"strings"
)

// SQLServerInspector provides SQL Server database introspection
type SQLServerInspector struct {
	db *boundedDB
}

// GetSchemas returns all non-system schemas in the database
func (i *SQLServerInspector) GetSchemas(ctx context.Context) ([]string, error) {
	query := `
		SELECT SCHEMA_NAME
		FROM INFORMATION_SCHEMA.SCHEMATA
		WHERE SCHEMA_NAME NOT IN ('sys', 'INFORMATION_SCHEMA', 'guest')
		AND SCHEMA_NAME NOT LIKE 'db[_]%'
		ORDER BY SCHEMA_NAME
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
func (i *SQLServerInspector) GetDefaultSchema(ctx context.Context) (string, error) {
	var schema string
	err := i.db.QueryRowContext(ctx, "SELECT SCHEMA_NAME()").Scan(&schema)
	return schema, err
}

// GetDatabaseName returns the current database name
func (i *SQLServerInspector) GetDatabaseName(ctx context.Context) (string, error) {
	var dbName string
	err := i.db.QueryRowContext(ctx, "SELECT DB_NAME()").Scan(&dbName)
	return dbName, err
}

// GetTables returns all tables or views in the specified schema
func (i *SQLServerInspector) GetTables(ctx context.Context, schema string, tableType string) ([]*Table, error) {
	query := `
		SELECT
			t.TABLE_SCHEMA,
			t.TABLE_NAME,
			t.TABLE_TYPE,
			CASE
				WHEN t.TABLE_TYPE = 'VIEW' THEN LEFT(OBJECT_DEFINITION(o.object_id), @p3)
				ELSE ''
			END AS VIEW_DEF,
			o.create_date
		FROM INFORMATION_SCHEMA.TABLES t
		LEFT JOIN sys.objects o
		  ON o.object_id = OBJECT_ID(t.TABLE_SCHEMA + '.' + t.TABLE_NAME)
		WHERE t.TABLE_SCHEMA = @p1
		AND t.TABLE_TYPE = CASE @p2
			WHEN 'table' THEN 'BASE TABLE'
			WHEN 'view' THEN 'VIEW'
			ELSE t.TABLE_TYPE
		END
		ORDER BY t.TABLE_NAME
	`

	rows, err := i.db.QueryContext(ctx, query, schema, tableType, i.db.definitionLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var tables []*Table
	for rows.Next() {
		var t Table
		var viewDef sql.NullString
		var createDate sql.NullTime
		if err := rows.Scan(&t.Schema, &t.Name, &t.Type, &viewDef, &createDate); err != nil {
			return nil, err
		}
		if viewDef.Valid {
			t.ViewDef = i.db.definition(viewDef.String)
		}
		if createDate.Valid {
			ts := createDate.Time
			t.CreateDate = &ts
		}
		tables = append(tables, &t)
	}
	return tables, rows.Err()
}

// GetColumns returns every column of every table and view in the schema.
//
// Beyond name/type/nullability it reads the facts a consumer cannot reconstruct
// afterwards: the character width and numeric precision, whether the column is
// part of the primary key, and whether the database generates its value. Those
// come from the same catalogue in the same round trip, so collecting them costs
// nothing extra and not collecting them forced every caller to ask again.
//
// The width/precision/scale columns deliberately come from INFORMATION_SCHEMA
// rather than sys.columns: sys.columns.max_length is in *bytes* (nvarchar is
// 2x), reports 0 instead of NULL for non-character types, and disagrees on
// text/ntext. Those three values flow straight into the emitted UIR, so the
// view that matches what consumers already assert on is the one to keep.
//
// sys.columns is joined only for is_identity, which replaces a per-row
// COLUMNPROPERTY(OBJECT_ID(...)) scalar call — harmless at table scope, tens of
// thousands of calls in a single statement at schema scope.
func (i *SQLServerInspector) GetColumns(ctx context.Context, schema string) ([]*Column, error) {
	query := `
		SELECT
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
			sc.is_identity
		FROM INFORMATION_SCHEMA.COLUMNS c
		LEFT JOIN sys.schemas ss ON ss.name = c.TABLE_SCHEMA
		LEFT JOIN sys.objects so ON so.schema_id = ss.schema_id AND so.name = c.TABLE_NAME
		LEFT JOIN sys.columns sc ON sc.object_id = so.object_id AND sc.name = c.COLUMN_NAME
		LEFT JOIN (
			SELECT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
			FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
			JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
			  ON ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			 AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
			 AND ku.TABLE_NAME = tc.TABLE_NAME
			WHERE tc.CONSTRAINT_TYPE = 'PRIMARY KEY'
			  AND tc.TABLE_SCHEMA = @p1
		) pk
		  ON pk.TABLE_SCHEMA = c.TABLE_SCHEMA
		 AND pk.TABLE_NAME = c.TABLE_NAME
		 AND pk.COLUMN_NAME = c.COLUMN_NAME
		LEFT JOIN (
			SELECT DISTINCT ku.TABLE_SCHEMA, ku.TABLE_NAME, ku.COLUMN_NAME
			FROM INFORMATION_SCHEMA.TABLE_CONSTRAINTS tc
			JOIN INFORMATION_SCHEMA.KEY_COLUMN_USAGE ku
			  ON ku.CONSTRAINT_NAME = tc.CONSTRAINT_NAME
			 AND ku.TABLE_SCHEMA = tc.TABLE_SCHEMA
			 AND ku.TABLE_NAME = tc.TABLE_NAME
			WHERE tc.CONSTRAINT_TYPE = 'UNIQUE' AND tc.TABLE_SCHEMA = @p1
		) uq ON uq.TABLE_SCHEMA = c.TABLE_SCHEMA
		 AND uq.TABLE_NAME = c.TABLE_NAME AND uq.COLUMN_NAME = c.COLUMN_NAME
		WHERE c.TABLE_SCHEMA = @p1
		ORDER BY c.TABLE_NAME, c.ORDINAL_POSITION
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var columns []*Column
	for rows.Next() {
		var c Column
		var isNullable, isPrimaryKey, isUnique int
		var defaultVal sql.NullString
		// CHARACTER_MAXIMUM_LENGTH is NULL for non-character types and -1 for
		// (max); precision/scale are NULL for non-numeric ones.
		var maxLength, precision, scale sql.NullInt64
		// is_identity is `bit`, so it must land in a bool — scanning a bit into
		// an int fails at the driver boundary (see GetIndexes). NULL when the
		// object join misses.
		var isIdentity sql.NullBool
		if err := rows.Scan(&c.TableName, &c.ColumnName, &c.DataType, &isNullable, &defaultVal, &c.Position,
			&maxLength, &precision, &scale, &isPrimaryKey, &isUnique, &isIdentity); err != nil {
			return nil, err
		}
		c.IsNullable = isNullable == 1
		c.IsPrimaryKey = isPrimaryKey == 1
		c.IsUnique = isUnique == 1
		c.IsAutoIncrement = isIdentity.Valid && isIdentity.Bool
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

// nullableInt keeps "the catalogue said nothing" distinct from "the catalogue
// said zero" — a non-character column has no width, which is not a width of 0.
func nullableInt(v sql.NullInt64) *int {
	if !v.Valid {
		return nil
	}
	n := int(v.Int64)
	return &n
}

// sqlServerIndexesQuery lists every named index in a schema with its key
// columns comma-joined in key_ordinal order.
//
// Ordered string aggregation is done with the FOR XML PATH idiom rather than
// STRING_AGG ... WITHIN GROUP: the WITHIN GROUP clause is rejected with
// "Incorrect syntax near '('" on a database whose compatibility level is below
// 140, whatever the engine version. OIPA databases routinely run at level 100.
// The `, TYPE).value(...)` projection keeps column names containing XML
// metacharacters intact and yields nvarchar(max), so a wide index does not hit
// an 8000-byte aggregate limit.
const sqlServerIndexesQuery = `
	SELECT
		t.name AS table_name,
		i.name AS index_name,
		i.is_unique,
		i.is_primary_key,
		STUFF((
			SELECT ',' + c.name
			FROM sys.index_columns ic2
			INNER JOIN sys.columns c ON ic2.object_id = c.object_id AND ic2.column_id = c.column_id
			WHERE ic2.object_id = i.object_id AND ic2.index_id = i.index_id AND ic2.key_ordinal > 0
			ORDER BY ic2.key_ordinal
			FOR XML PATH(''), TYPE
		).value('.', 'nvarchar(max)'), 1, 1, '') AS columns,
		i.type_desc,
		COALESCE(i.filter_definition, '')
	FROM sys.indexes i
	INNER JOIN sys.tables t ON i.object_id = t.object_id
	INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
	WHERE s.name = @p1 AND i.name IS NOT NULL
	ORDER BY t.name, i.name
`

// GetIndexes returns every named index on every table in the schema.
//
// Included columns are excluded because Columns describes the ordered key.
// Views are absent by construction: the join is to sys.tables.
func (i *SQLServerInspector) GetIndexes(ctx context.Context, schema string) ([]*Index, error) {
	rows, err := i.db.QueryContext(ctx, sqlServerIndexesQuery, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var indexes []*Index
	for rows.Next() {
		var idx Index
		var columnsStr sql.NullString
		// is_unique / is_primary_key are `bit`, which go-mssqldb decodes to a
		// Go bool. Scanning them into an int makes database/sql stringify the
		// bool and ParseInt("true"), which fails on the first row of every
		// indexed table — so scan straight into the bool fields.
		if err := rows.Scan(&idx.TableName, &idx.IndexName, &idx.IsUnique, &idx.IsPrimary, &columnsStr, &idx.Type, &idx.Condition); err != nil {
			return nil, err
		}
		if columnsStr.Valid && columnsStr.String != "" {
			idx.Columns = strings.Split(columnsStr.String, ",")
		}
		indexes = append(indexes, &idx)
	}
	return indexes, rows.Err()
}

// GetForeignKeys returns every foreign key column in the schema, one row per
// participating column.
//
// The ordering by fkc.constraint_column_id is load-bearing, not tidiness: the
// caller pairs a constraint's local and referenced columns positionally, so
// rows arriving in an arbitrary order emit a composite key whose columns are
// matched to the wrong targets. constraint_column_id is that position.
func (i *SQLServerInspector) GetForeignKeys(ctx context.Context, schema string) ([]*ForeignKey, error) {
	query := `
		SELECT
			t1.name AS table_name,
			fk.name AS constraint_name,
			c1.name AS column_name,
			s2.name AS ref_schema_name,
			t2.name AS ref_table_name,
			c2.name AS ref_column_name
		FROM sys.foreign_keys fk
		INNER JOIN sys.foreign_key_columns fkc ON fk.object_id = fkc.constraint_object_id
		INNER JOIN sys.tables t1 ON fk.parent_object_id = t1.object_id
		INNER JOIN sys.schemas s1 ON t1.schema_id = s1.schema_id
		INNER JOIN sys.columns c1 ON fkc.parent_object_id = c1.object_id AND fkc.parent_column_id = c1.column_id
		INNER JOIN sys.tables t2 ON fk.referenced_object_id = t2.object_id
		INNER JOIN sys.schemas s2 ON t2.schema_id = s2.schema_id
		INNER JOIN sys.columns c2 ON fkc.referenced_object_id = c2.object_id AND fkc.referenced_column_id = c2.column_id
		WHERE s1.name = @p1
		ORDER BY t1.name, fk.name, fkc.constraint_column_id
	`

	rows, err := i.db.QueryContext(ctx, query, schema)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var fks []*ForeignKey
	for rows.Next() {
		var fk ForeignKey
		if err := rows.Scan(&fk.TableName, &fk.ConstraintName, &fk.ColumnName, &fk.RefSchemaName, &fk.RefTableName, &fk.RefColumnName); err != nil {
			return nil, err
		}
		fks = append(fks, &fk)
	}
	return fks, rows.Err()
}

// GetStoredProcs returns all stored procedures and functions in the specified schema
func (i *SQLServerInspector) GetStoredProcs(ctx context.Context, schema string) ([]*StoredProc, error) {
	// sys.parameters exposes the function return type as parameter_id = 0
	// (scalar functions). Inline/table-valued functions have no return row
	// there, so we fall back to 'TABLE' for IF/TF via the CASE expression.
	query := `
		SELECT
			s.name AS schema_name,
			o.name AS proc_name,
			CASE o.type
				WHEN 'P' THEN 'procedure'
				WHEN 'FN' THEN 'function'
				WHEN 'IF' THEN 'function'
				WHEN 'TF' THEN 'function'
				ELSE 'procedure'
			END AS proc_type,
			LEFT(OBJECT_DEFINITION(o.object_id), @p2) AS proc_sql,
			o.create_date,
			CASE
				WHEN o.type = 'FN' THEN (
					SELECT TOP 1 t.name
					FROM sys.parameters p
					INNER JOIN sys.types t ON p.user_type_id = t.user_type_id
					WHERE p.object_id = o.object_id AND p.parameter_id = 0
				)
				WHEN o.type IN ('IF', 'TF') THEN 'TABLE'
				ELSE NULL
			END AS return_type
		FROM sys.objects o
		INNER JOIN sys.schemas s ON o.schema_id = s.schema_id
		WHERE s.name = @p1
		AND o.type IN ('P', 'FN', 'IF', 'TF')
		ORDER BY o.name
	`

	rows, err := i.db.QueryContext(ctx, query, schema, i.db.definitionLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var procs []*StoredProc
	for rows.Next() {
		var p StoredProc
		var procSQL, returnType sql.NullString
		var createDate sql.NullTime
		if err := rows.Scan(&p.Schema, &p.Name, &p.Type, &procSQL, &createDate, &returnType); err != nil {
			return nil, err
		}
		if procSQL.Valid {
			p.SQL = i.db.definition(procSQL.String)
		}
		p.ID = p.Name
		if returnType.Valid {
			p.ReturnType = returnType.String
		}
		if createDate.Valid {
			ts := createDate.Time
			p.CreateDate = &ts
		}
		procs = append(procs, &p)
	}
	return procs, rows.Err()
}

// sqlServerTriggersQuery lists every DML trigger in a schema with its firing
// events comma-joined in type_desc order. Same FOR XML PATH idiom as
// sqlServerIndexesQuery, for the same compatibility-level reason.
const sqlServerTriggersQuery = `
	SELECT
		s.name          AS schema_name,
		tr.name         AS trigger_name,
		t.name          AS table_name,
		STUFF((
			SELECT ',' + te.type_desc
			FROM sys.trigger_events te
			WHERE te.object_id = tr.object_id
			ORDER BY te.type_desc
			FOR XML PATH(''), TYPE
		).value('.', 'nvarchar(max)'), 1, 1, '') AS events,
		CASE WHEN tr.is_instead_of_trigger = 1 THEN 'INSTEAD OF' ELSE 'AFTER' END AS timing,
		tr.is_disabled,
		LEFT(OBJECT_DEFINITION(tr.object_id), @p2) AS trigger_sql,
		tr.create_date
	FROM sys.triggers tr
	INNER JOIN sys.objects t ON tr.parent_id = t.object_id
	INNER JOIN sys.schemas s ON t.schema_id = s.schema_id
	WHERE s.name = @p1
	  AND tr.parent_class = 1
	ORDER BY t.name, tr.name
`

// GetTriggers returns all DML triggers in the given schema, grouped by
// (schema, name). A single trigger firing on multiple events (e.g.
// INSERT+UPDATE) is returned as one row with a comma-joined Event string.
func (i *SQLServerInspector) GetTriggers(ctx context.Context, schema string) ([]*Trigger, error) {
	rows, err := i.db.QueryContext(ctx, sqlServerTriggersQuery, schema, i.db.definitionLimit())
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var triggers []*Trigger
	for rows.Next() {
		var tr Trigger
		var events, body sql.NullString
		var createDate sql.NullTime
		// is_disabled is `bit` — see the note in GetIndexes. Scan it as a bool.
		if err := rows.Scan(&tr.Schema, &tr.Name, &tr.TableName, &events, &tr.Timing, &tr.IsDisabled, &body, &createDate); err != nil {
			return nil, err
		}
		if events.Valid {
			tr.Event = events.String
		}
		if body.Valid {
			tr.SQL = i.db.definition(body.String)
		}
		if createDate.Valid {
			ts := createDate.Time
			tr.CreateDate = &ts
		}
		triggers = append(triggers, &tr)
	}
	return triggers, rows.Err()
}

// GetProcParams returns the parameters of every stored procedure and function
// in the schema.
//
// parameter_id > 0 excludes a scalar function's return value, which sys.parameters
// reports as an unnamed parameter at id 0. Emitting it as Params[0] produced a
// parameter with an empty name that also duplicated what GetStoredProcs already
// reports as ReturnType.
//
// The object-type filter mirrors GetStoredProcs so only objects that can be
// emitted are queried. It is not a narrowing: object names are unique per schema
// in SQL Server, so the old `o.name = @p2` predicate matched the same single row.
func (i *SQLServerInspector) GetProcParams(ctx context.Context, schema string) ([]*ProcParam, error) {
	query := `
		SELECT
			o.name AS proc_name,
			p.name AS param_name,
			t.name AS data_type,
			p.parameter_id AS position
		FROM sys.parameters p
		INNER JOIN sys.types t ON p.user_type_id = t.user_type_id
		INNER JOIN sys.objects o ON p.object_id = o.object_id
		INNER JOIN sys.schemas s ON o.schema_id = s.schema_id
		WHERE s.name = @p1
		AND o.type IN ('P', 'FN', 'IF', 'TF')
		AND p.parameter_id > 0
		ORDER BY o.name, p.parameter_id
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
		p.ProcID = p.ProcName
		params = append(params, &p)
	}
	return params, rows.Err()
}
