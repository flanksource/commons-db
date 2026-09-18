// Package sqlite reconciles SQLite tables with a declaration through Atlas,
// as the parent migrate package reconciles PostgreSQL schemas with HCL. It
// works over a caller's own connection or transaction, so it serves the pure-Go
// modernc driver without Atlas opening one of its own.
package sqlite

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"ariga.io/atlas/sql/schema"
	atlas "ariga.io/atlas/sql/sqlite"
	"github.com/flanksource/commons/logger"
)

// Apply brings database's tables up to the tables declared: Atlas inspects
// each declared table, diffs it against its declaration, and applies the plan.
// It only adds — a missing table, column or index. Every other difference (a
// column dropped, retyped or made NOT NULL, a key or an index changed) would
// rewrite or discard rows the declaration no longer describes, so Apply refuses
// it before applying anything, naming each change. Tables and views nothing
// declares are neither inspected nor touched.
//
// A column may be declared by its raw SQLite type alone (ColumnType.Raw); Apply
// parses it as inspection does, so a declaration and the table it created
// compare equal.
func Apply(ctx context.Context, database schema.ExecQuerier, tables ...*schema.Table) error {
	if len(tables) == 0 {
		return errors.New("sqlite migrate: no table declared")
	}
	driver, err := atlas.Open(database)
	if err != nil {
		return fmt.Errorf("sqlite migrate: open driver: %w", err)
	}
	names := make([]string, len(tables))
	for index, table := range tables {
		if err := parseRawTypes(table); err != nil {
			return err
		}
		names[index] = table.Name
	}
	current, err := driver.InspectSchema(ctx, "", &schema.InspectOptions{Mode: schema.InspectTables, Tables: names})
	if err != nil {
		return fmt.Errorf("sqlite migrate: inspect %s: %w", strings.Join(names, ", "), err)
	}
	desired := schema.New(current.Name).AddTables(tables...)
	changes, err := driver.SchemaDiff(current, desired)
	if err != nil {
		return fmt.Errorf("sqlite migrate: diff %s: %w", strings.Join(names, ", "), err)
	}
	if err := refuseRewrites(changes); err != nil {
		return err
	}
	if len(changes) == 0 {
		return nil
	}
	plan, err := driver.PlanChanges(ctx, "", changes)
	if err != nil {
		return fmt.Errorf("sqlite migrate: plan %d changes: %w", len(changes), err)
	}
	log := logger.GetLogger("migrate")
	for _, change := range plan.Changes {
		log.Tracef("%s", change.Cmd)
	}
	if err := driver.ApplyChanges(ctx, changes); err != nil {
		return fmt.Errorf("sqlite migrate: apply %d changes: %w", len(changes), err)
	}
	log.V(1).Infof("Applied %d sqlite schema changes to %s", len(changes), strings.Join(names, ", "))
	return nil
}

// parseRawTypes gives every column declared by its raw type alone the type
// Atlas's own inspection parses that raw type into.
func parseRawTypes(table *schema.Table) error {
	for _, column := range table.Columns {
		if column.Type == nil {
			return fmt.Errorf("sqlite migrate: table %q column %q declares no type", table.Name, column.Name)
		}
		if column.Type.Type != nil {
			continue
		}
		parsed, err := atlas.ParseType(column.Type.Raw)
		if err != nil {
			return fmt.Errorf("sqlite migrate: table %q column %q type %q: %w", table.Name, column.Name, column.Type.Raw, err)
		}
		column.Type.Type = parsed
	}
	return nil
}

// refuseRewrites fails on any change other than an added table, column or
// index, listing every such change by table.
func refuseRewrites(changes []schema.Change) error {
	var refused []string
	for _, change := range changes {
		switch change := change.(type) {
		case *schema.AddTable:
		case *schema.ModifyTable:
			var described []string
			for _, inner := range change.Changes {
				switch inner.(type) {
				case *schema.AddColumn, *schema.AddIndex:
				default:
					described = append(described, describe(inner))
				}
			}
			if len(described) > 0 {
				refused = append(refused, fmt.Sprintf("table %q: %s", change.T.Name, strings.Join(described, ", ")))
			}
		default:
			refused = append(refused, describe(change))
		}
	}
	if len(refused) == 0 {
		return nil
	}
	return fmt.Errorf("sqlite migrate: refusing %s; only added tables, columns and indexes are applied, since any other change rewrites or discards stored rows",
		strings.Join(refused, "; "))
}

func describe(change schema.Change) string {
	switch change := change.(type) {
	case *schema.DropColumn:
		return fmt.Sprintf("drop column %q", change.C.Name)
	case *schema.ModifyColumn:
		return fmt.Sprintf("change column %q", change.From.Name)
	case *schema.RenameColumn:
		return fmt.Sprintf("rename column %q to %q", change.From.Name, change.To.Name)
	case *schema.DropIndex:
		return fmt.Sprintf("drop index %q", change.I.Name)
	case *schema.ModifyIndex:
		return fmt.Sprintf("change index %q", change.From.Name)
	case *schema.RenameIndex:
		return fmt.Sprintf("rename index %q to %q", change.From.Name, change.To.Name)
	case *schema.AddPrimaryKey:
		return "add primary key"
	case *schema.DropPrimaryKey:
		return "drop primary key"
	case *schema.ModifyPrimaryKey:
		return "change primary key"
	case *schema.DropTable:
		return fmt.Sprintf("drop table %q", change.T.Name)
	default:
		return fmt.Sprintf("%T", change)
	}
}
