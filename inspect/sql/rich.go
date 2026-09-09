package sqlinspect

import (
	"context"
	"database/sql"
	"fmt"

	"github.com/flanksource/commons-db/inspect/sql/inspector"
)

// inspectRich assembles schema-scoped rows into the browser catalog. Optional
// metadata budgets must not suppress table/column browsing in later schemas.
func inspectRich(ctx context.Context, db *sql.DB, driver string, limits Limits) (Catalog, error) {
	limits = limits.withDefaults()
	bounds := &inspector.Bounds{Rows: limits.MaxRelations, DefinitionBytes: limits.MaxDefinitionBytes}
	i, err := inspector.NewInspector(db, driver, bounds)
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

	catalog := Catalog{Driver: driver, Database: database, Databases: databases, DefaultSchema: defaultSchema, Schemas: []Schema{}}
	budget := richBudget{relations: limits.MaxRelations, columns: limits.MaxColumns, routines: limits.MaxRoutines, metadata: limits.MaxColumns}
	for _, schemaName := range schemaNames {
		if budget.relations == 0 || budget.columns == 0 {
			bounds.Truncated = true
			break
		}
		schema, err := inspectRichSchema(ctx, i, schemaName, &budget, bounds)
		if err != nil {
			return Catalog{}, fmt.Errorf("inspect sql schema %q: %w", schemaName, err)
		}
		catalog.Schemas = append(catalog.Schemas, schema)
	}
	catalog.Truncated = bounds.Truncated
	if catalog.Truncated {
		catalog.TruncateReason = fmt.Sprintf("catalog limits reached: %d relations, %d columns, %d routines, %d metadata rows, %d definition bytes", limits.MaxRelations, limits.MaxColumns, limits.MaxRoutines, limits.MaxColumns, limits.MaxDefinitionBytes)
	}
	return catalog, nil
}

type richBudget struct {
	relations, columns, routines, metadata int
}

// readCatalog bounds each query by the remaining global budget, not a fresh
// allowance per schema. The inspector detects overflow with a lookahead row.
func readCatalog[T any](bounds *inspector.Bounds, remaining *int, read func() ([]*T, error)) ([]*T, error) {
	bounds.Rows = *remaining
	items, err := read()
	*remaining -= len(items)
	return items, err
}

func inspectRichSchema(ctx context.Context, i inspector.Inspector, schemaName string, budget *richBudget, bounds *inspector.Bounds) (Schema, error) {
	tables, err := readCatalog(bounds, &budget.relations, func() ([]*inspector.Table, error) { return i.GetTables(ctx, schemaName, "table") })
	if err != nil {
		return Schema{}, err
	}
	views, err := readCatalog(bounds, &budget.relations, func() ([]*inspector.Table, error) { return i.GetTables(ctx, schemaName, "view") })
	if err != nil {
		return Schema{}, err
	}
	columns, err := readCatalog(bounds, &budget.columns, func() ([]*inspector.Column, error) { return i.GetColumns(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}
	indexes, err := readCatalog(bounds, &budget.metadata, func() ([]*inspector.Index, error) { return i.GetIndexes(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}
	foreignKeys, err := readCatalog(bounds, &budget.metadata, func() ([]*inspector.ForeignKey, error) { return i.GetForeignKeys(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}
	triggers, err := readCatalog(bounds, &budget.metadata, func() ([]*inspector.Trigger, error) { return i.GetTriggers(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}
	routines, err := readCatalog(bounds, &budget.routines, func() ([]*inspector.StoredProc, error) { return i.GetStoredProcs(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}
	params, err := readCatalog(bounds, &budget.metadata, func() ([]*inspector.ProcParam, error) { return i.GetProcParams(ctx, schemaName) })
	if err != nil {
		return Schema{}, err
	}

	schema := Schema{Name: schemaName, Relations: []Relation{}}
	allTables := append(tables, views...)
	for _, table := range allTables {
		typeName := normalizeRelationType(table.Type)
		schema.Relations = append(schema.Relations, Relation{Name: table.Name, Type: typeName, ViewDef: table.ViewDef, Columns: []Column{}})
	}
	relations := make(map[string]*Relation, len(schema.Relations))
	for index := range schema.Relations {
		relations[schema.Relations[index].Name] = &schema.Relations[index]
	}
	for _, column := range columns {
		relation := relations[column.TableName]
		if relation == nil {
			continue
		}
		relation.Columns = append(relation.Columns, Column{Name: column.ColumnName, DataType: column.DataType, Ordinal: column.Position, Nullable: &column.IsNullable, Default: column.DefaultValue, Identity: column.IsAutoIncrement, PrimaryKey: column.IsPrimaryKey, Unique: column.IsUnique, MaxLength: column.MaxLength, NumericPrecision: column.NumericPrecision, NumericScale: column.NumericScale, Comment: column.Comment, EnumValues: column.EnumValues})
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
			relation.ForeignKeys = append(relation.ForeignKeys, ForeignKey{Name: fk.ConstraintName, ReferencedSchema: fk.RefSchemaName, ReferencedTable: fk.RefTableName})
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
		paramsByRoutine[param.ProcID] = append(paramsByRoutine[param.ProcID], RoutineParameter{Name: param.ParamName, DataType: param.DataType, Ordinal: param.Position})
	}
	for _, routine := range routines {
		schema.Routines = append(schema.Routines, Routine{ID: routine.ID, Name: routine.Name, Type: routine.Type, SQL: routine.SQL, ReturnType: routine.ReturnType, Parameters: paramsByRoutine[routine.ID]})
	}
	return schema, nil
}
