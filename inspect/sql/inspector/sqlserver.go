package inspector

import (
	"context"
	"database/sql"

	"github.com/flanksource/commons-db/internal/mssqlcatalog"
)

// SQLServerInspector provides SQL Server database introspection. Catalog SQL is
// owned by internal/mssqlcatalog; this type maps its rows onto the inspector
// models and applies the inspection's row and definition bounds.
type SQLServerInspector struct {
	db *boundedDB
}

// boundedCatalog runs mssqlcatalog queries through the inspection's bounds.
type boundedCatalog struct{ db *boundedDB }

func (c boundedCatalog) QueryContext(ctx context.Context, query string, args ...any) (mssqlcatalog.Rows, error) {
	rows, err := c.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	return rows, nil
}

func (i *SQLServerInspector) catalog() mssqlcatalog.Queryer { return boundedCatalog{i.db} }

func (i *SQLServerInspector) scope(schema string) mssqlcatalog.Scope {
	return mssqlcatalog.Scope{Schema: schema, DefinitionLimit: i.db.definitionLimit()}
}

// GetSchemas returns all non-system schemas in the database
func (i *SQLServerInspector) GetSchemas(ctx context.Context) ([]string, error) {
	return mssqlcatalog.Schemas(ctx, i.catalog(), nil)
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
	rows, err := mssqlcatalog.Tables(ctx, i.catalog(), i.scope(schema), mssqlcatalog.TableType(tableType))
	if err != nil {
		return nil, err
	}
	var tables []*Table
	for _, row := range rows {
		t := &Table{Schema: row.Schema, Name: row.Name, Type: row.Type}
		if row.Definition != nil {
			t.ViewDef = i.db.definition(*row.Definition)
		}
		tables = append(tables, t)
	}
	return tables, nil
}

// GetColumns returns every column of every table and view in the schema.
//
// Widths, precision and scale are the INFORMATION_SCHEMA character values, not
// sys.columns byte sizes: the browser needs character widths.
func (i *SQLServerInspector) GetColumns(ctx context.Context, schema string) ([]*Column, error) {
	rows, err := mssqlcatalog.Columns(ctx, i.catalog(), i.scope(schema))
	if err != nil {
		return nil, err
	}
	var columns []*Column
	for _, row := range rows {
		columns = append(columns, &Column{
			TableName: row.Table, ColumnName: row.Name, DataType: row.DataType,
			IsNullable: row.Nullable, DefaultValue: row.Default, Position: row.Position,
			IsAutoIncrement: row.Identity, IsPrimaryKey: row.PrimaryKey, IsUnique: row.Unique,
			MaxLength: row.CharMaxLength, NumericPrecision: row.NumericPrecision, NumericScale: row.NumericScale,
		})
	}
	return columns, nil
}

// GetIndexes returns every named index on every table in the schema. Columns
// is the ordered key; included columns are excluded.
func (i *SQLServerInspector) GetIndexes(ctx context.Context, schema string) ([]*Index, error) {
	rows, err := mssqlcatalog.Indexes(ctx, i.catalog(), i.scope(schema))
	if err != nil {
		return nil, err
	}
	var indexes []*Index
	for _, row := range rows {
		indexes = append(indexes, &Index{
			TableName: row.Table, IndexName: row.Name, IsUnique: row.Unique, IsPrimary: row.Primary,
			Columns: row.KeyColumns, Type: row.TypeDesc, Condition: row.Filter,
		})
	}
	return indexes, nil
}

// GetForeignKeys returns every foreign key column in the schema, one row per
// participating column, in constraint column order.
func (i *SQLServerInspector) GetForeignKeys(ctx context.Context, schema string) ([]*ForeignKey, error) {
	rows, err := mssqlcatalog.ForeignKeys(ctx, i.catalog(), i.scope(schema))
	if err != nil {
		return nil, err
	}
	var fks []*ForeignKey
	for _, row := range rows {
		fks = append(fks, &ForeignKey{
			TableName: row.Table, ConstraintName: row.Name, ColumnName: row.Column,
			RefSchemaName: row.RefSchema, RefTableName: row.RefTable, RefColumnName: row.RefColumn,
		})
	}
	return fks, nil
}

// GetStoredProcs returns all stored procedures and functions in the specified schema
func (i *SQLServerInspector) GetStoredProcs(ctx context.Context, schema string) ([]*StoredProc, error) {
	rows, err := mssqlcatalog.Routines(ctx, i.catalog(), i.scope(schema))
	if err != nil {
		return nil, err
	}
	var procs []*StoredProc
	for _, row := range rows {
		p := &StoredProc{ID: row.Name, Schema: row.Schema, Name: row.Name, Type: "procedure"}
		if row.Type.IsFunction() {
			p.Type = "function"
		}
		if row.Definition != nil {
			p.SQL = i.db.definition(*row.Definition)
		}
		if row.ReturnType != nil {
			p.ReturnType = *row.ReturnType
		}
		procs = append(procs, p)
	}
	return procs, nil
}

// sqlServerTriggersQuery lists every DML trigger in a schema with its firing
// events comma-joined in type_desc order. Ordered string aggregation uses the
// FOR XML PATH idiom because STRING_AGG ... WITHIN GROUP is rejected below
// compatibility level 140 (OIPA databases run at level 100).
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
		LEFT(OBJECT_DEFINITION(tr.object_id), @p2) AS trigger_sql
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
		// is_disabled is `bit`, which go-mssqldb decodes to a Go bool; scan it as a bool.
		if err := rows.Scan(&tr.Schema, &tr.Name, &tr.TableName, &events, &tr.Timing, &tr.IsDisabled, &body); err != nil {
			return nil, err
		}
		if events.Valid {
			tr.Event = events.String
		}
		if body.Valid {
			tr.SQL = i.db.definition(body.String)
		}
		triggers = append(triggers, &tr)
	}
	return triggers, rows.Err()
}

// GetProcParams returns the parameters of every stored procedure and function
// in the schema. A scalar function's return value (parameter_id 0) is not a
// parameter; GetStoredProcs reports it as ReturnType.
func (i *SQLServerInspector) GetProcParams(ctx context.Context, schema string) ([]*ProcParam, error) {
	rows, err := mssqlcatalog.Parameters(ctx, i.catalog(), i.scope(schema))
	if err != nil {
		return nil, err
	}
	var params []*ProcParam
	for _, row := range rows {
		params = append(params, &ProcParam{
			ProcID: row.Routine, ProcName: row.Routine, ParamName: row.Name, DataType: row.DataType, Position: row.Position,
		})
	}
	return params, nil
}
