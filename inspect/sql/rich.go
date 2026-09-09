package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/inspect/sql/inspector"
)

// inspectRich adapts the schema-scoped arch-unit inspector contract to the
// browser catalog while retaining this package's global relation/column caps.
func inspectRich(ctx context.Context, db *sql.DB, driver string, limits Limits) (Catalog, error) {
	i, err := inspector.NewInspector(db, driver)
	if err != nil {
		return Catalog{}, err
	}
	database, err := i.GetDatabaseName(ctx)
	if err != nil {
		return Catalog{}, fmt.Errorf("inspect sql database: %w", err)
	}
	defaultSchema, err := i.GetDefaultSchema(ctx)
	if err != nil {
		return Catalog{}, fmt.Errorf("inspect sql default schema: %w", err)
	}
	databases, err := ListDatabases(ctx, db, driver)
	if err != nil {
		return Catalog{}, err
	}
	schemaNames, err := i.GetSchemas(ctx)
	if err != nil {
		return Catalog{}, fmt.Errorf("inspect sql schemas: %w", err)
	}

	limits = limits.withDefaults()
	catalog := Catalog{Driver: driver, Database: database, Databases: databases, DefaultSchema: defaultSchema, Schemas: []Schema{}}
	relationCount, columnCount := 0, 0
	for _, schemaName := range schemaNames {
		schema, relationsSeen, columnsSeen, truncated, reason, err := inspectRichSchema(ctx, i, schemaName, limits.MaxRelations-relationCount, limits.MaxColumns-columnCount)
		if err != nil {
			return Catalog{}, fmt.Errorf("inspect sql schema %q: %w", schemaName, err)
		}
		catalog.Schemas = append(catalog.Schemas, schema)
		relationCount += relationsSeen
		columnCount += columnsSeen
		if truncated {
			catalog.Truncated, catalog.TruncateReason = true, reason
		}
	}
	return catalog, nil
}

func inspectRichSchema(ctx context.Context, i inspector.Inspector, schemaName string, relationBudget, columnBudget int) (Schema, int, int, bool, string, error) {
	tables, err := i.GetTables(ctx, schemaName, "table")
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	views, err := i.GetTables(ctx, schemaName, "view")
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	columns, err := i.GetColumns(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	indexes, err := i.GetIndexes(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	foreignKeys, err := i.GetForeignKeys(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	triggers, err := i.GetTriggers(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	routines, err := i.GetStoredProcs(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}
	params, err := i.GetProcParams(ctx, schemaName)
	if err != nil {
		return Schema{}, 0, 0, false, "", err
	}

	schema := Schema{Name: schemaName, Relations: []Relation{}}
	allTables := append(tables, views...)
	truncated, reason := false, ""
	for _, table := range allTables {
		if len(schema.Relations) >= max(relationBudget, 0) {
			truncated, reason = true, fmt.Sprintf("relation limit %d reached", relationBudget)
			continue
		}
		typeName := normalizeRelationType(table.Type)
		schema.Relations = append(schema.Relations, Relation{Name: table.Name, Type: typeName, ViewDef: table.ViewDef, Columns: []Column{}})
	}
	relations := make(map[string]*Relation, len(schema.Relations))
	for index := range schema.Relations {
		relations[schema.Relations[index].Name] = &schema.Relations[index]
	}
	columnsUsed := 0
	for _, column := range columns {
		relation := relations[column.TableName]
		if relation == nil {
			continue
		}
		if columnsUsed >= max(columnBudget, 0) {
			truncated, reason = true, fmt.Sprintf("column limit %d reached", columnBudget)
			continue
		}
		relation.Columns = append(relation.Columns, Column{Name: column.ColumnName, DataType: column.DataType, Ordinal: column.Position, Nullable: &column.IsNullable, Default: column.DefaultValue, Identity: column.IsAutoIncrement, PrimaryKey: column.IsPrimaryKey, Unique: column.IsUnique, MaxLength: column.MaxLength, NumericPrecision: column.NumericPrecision, NumericScale: column.NumericScale, Comment: column.Comment, EnumValues: column.EnumValues})
		columnsUsed++
	}
	for _, index := range indexes {
		if relation := relations[index.TableName]; relation != nil {
			relation.Indexes = append(relation.Indexes, Index{Name: index.IndexName, Unique: index.IsUnique, Primary: index.IsPrimary, Columns: index.Columns, Type: index.Type, Filter: index.Condition})
		}
	}
	for _, fk := range foreignKeys {
		relation := relations[fk.TableName]
		if relation == nil {
			continue
		}
		if len(relation.ForeignKeys) == 0 || relation.ForeignKeys[len(relation.ForeignKeys)-1].Name != fk.ConstraintName {
			relation.ForeignKeys = append(relation.ForeignKeys, ForeignKey{Name: fk.ConstraintName, ReferencedTable: fk.RefTableName})
		}
		group := &relation.ForeignKeys[len(relation.ForeignKeys)-1]
		group.Columns = append(group.Columns, fk.ColumnName)
		group.ReferencedColumns = append(group.ReferencedColumns, fk.RefColumnName)
	}
	for _, trigger := range triggers {
		if relation := relations[trigger.TableName]; relation != nil {
			relation.Triggers = append(relation.Triggers, Trigger{Name: trigger.Name, Event: trigger.Event, Timing: trigger.Timing, Disabled: trigger.IsDisabled, SQL: trigger.SQL})
		}
	}
	paramsByRoutine := map[string][]RoutineParameter{}
	for _, param := range params {
		paramsByRoutine[param.ProcName] = append(paramsByRoutine[param.ProcName], RoutineParameter{Name: param.ParamName, DataType: param.DataType, Ordinal: param.Position})
	}
	for _, routine := range routines {
		schema.Routines = append(schema.Routines, Routine{Name: routine.Name, Type: routine.Type, SQL: routine.SQL, ReturnType: routine.ReturnType, Parameters: paramsByRoutine[routine.Name]})
	}
	return schema, len(schema.Relations), columnsUsed, truncated, reason, nil
}
