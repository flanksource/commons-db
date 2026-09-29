// Dynamic kinds in a sqlite file: a column added for every key a row brings,
// typed by its first value, and every column the catalog records adopted.
package sqlite

import (
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"github.com/flanksource/commons-db/query"
	"github.com/flanksource/commons-db/recordstore"
)

// rowsFor are rows as table stores them. For a dynamic kind table first
// gains a column for every key the rows bring that it lacks, and the table
// returned is the widened one.
func (b *Backend) rowsFor(ctx context.Context, table kindTable, rows []recordstore.Row) (kindTable, []query.Row, error) {
	if table.schema.Options.Dynamic {
		added, err := inferColumns(table, rows)
		if err != nil {
			return kindTable{}, nil, err
		}
		if len(added) > 0 {
			if table, err = b.addInferred(ctx, table, added); err != nil {
				return kindTable{}, nil, err
			}
		}
		rows = withoutUnknownNulls(table, rows)
	}
	stored, err := storedRows(table.Table, table.schema.Kind, rows)
	return table, stored, err
}

// inferColumns are the columns rows bring that table lacks, typed by
// recordstore.InferColumnType and sorted by name. A value whose type differs
// from its inferred column's, or two types for one new column, refuse the
// rows with recordstore.ErrSchemaMismatch; more inferred columns than the kind
// allows refuse them with recordstore.ErrCapacity. A declared column keeps the
// types its declaration accepts.
func inferColumns(table kindTable, rows []recordstore.Row) ([]query.ColumnDef, error) {
	types := make(map[string]query.ColumnType, len(table.Columns))
	for _, column := range table.Columns {
		types[column.Name] = column.Type
	}
	declared := func(name string) bool {
		return name == streamColumn || name == seqColumn ||
			slices.ContainsFunc(table.schema.Columns, func(column query.ColumnDef) bool { return column.Name == name })
	}
	inferred := map[string]query.ColumnType{}
	for index, row := range rows {
		for key, value := range row {
			found, ok := recordstore.InferColumnType(value)
			if !ok || declared(key) {
				continue
			}
			expected, known := types[key]
			if !known {
				expected, known = inferred[key]
			}
			if known && expected != found {
				return nil, fmt.Errorf("kind %q row %d key %q holds a %s, but its column is a %s: %w",
					table.schema.Kind, index, key, found, expected, recordstore.ErrSchemaMismatch)
			}
			if _, stored := types[key]; !stored {
				inferred[key] = found
			}
		}
	}
	added := make([]query.ColumnDef, 0, len(inferred))
	for name, columnType := range inferred {
		added = append(added, query.ColumnDef{Name: name, Type: columnType})
	}
	slices.SortFunc(added, func(a, b query.ColumnDef) int {
		if a.Name < b.Name {
			return -1
		}
		if a.Name > b.Name {
			return 1
		}
		return 0
	})
	existing := len(table.Columns) - len(storeColumns) - len(table.schema.Columns)
	if limit := table.schema.Options.DynamicColumnLimit(); existing+len(added) > limit {
		return nil, fmt.Errorf("kind %q would infer %d columns, more than its limit of %d: %w",
			table.schema.Kind, existing+len(added), limit, recordstore.ErrCapacity)
	}
	return added, nil
}

// addInferred widens table in the file with the added columns and returns it.
// The cached table is dropped, so the next resolution adopts every column the
// catalog records, including ones another append added meanwhile.
func (b *Backend) addInferred(ctx context.Context, table kindTable, added []query.ColumnDef) (kindTable, error) {
	widened := table
	widened.Columns = append(slices.Clone(table.Columns), added...)
	widened.StoredAs = nil
	widened, err := b.reconcileTable(ctx, table.schema.Kind, widened)
	if err != nil {
		return kindTable{}, err
	}
	b.tablesMu.Lock()
	delete(b.tables, table.schema.Kind)
	b.tablesMu.Unlock()
	return widened, nil
}

// withInferred is table with, for a dynamic kind, every column the catalog
// records that the kind does not declare.
func (table kindTable) withInferred(catalog storedCatalog) kindTable {
	if !table.schema.Options.Dynamic {
		return table
	}
	columns := slices.Clone(table.Columns)
	for _, stored := range catalog.columns {
		if !slices.ContainsFunc(columns, func(column query.ColumnDef) bool { return column.Name == stored.declared }) {
			columns = append(columns, stored.def())
			if stored.compressed() {
				table.Compressed = append(slices.Clone(table.Compressed), stored.declared)
			}
		}
	}
	table.Columns = columns
	return table
}

// catalogColumns is how many columns kind's catalog entry records, for a
// cached dynamic table to notice columns another process inferred.
func (b *Backend) catalogColumns(ctx context.Context, kind string) (int, error) {
	stored, _, found, err := kindEntry(ctx, b.database.Reader(), kind)
	if err != nil || !found {
		return 0, err
	}
	var parts []string
	if err := json.Unmarshal([]byte(stored), &parts); err != nil {
		return 0, fmt.Errorf("kind %q: decode catalog columns: %w", kind, err)
	}
	count := 0
	for _, part := range parts {
		if _, ok := parseStoredColumn(part); ok {
			count++
		}
	}
	return count, nil
}

// withoutUnknownNulls drops the null values of keys table has no column for:
// a null infers no column, and storing it would store nothing.
func withoutUnknownNulls(table kindTable, rows []recordstore.Row) []recordstore.Row {
	kept := make([]recordstore.Row, len(rows))
	for index, row := range rows {
		kept[index] = make(recordstore.Row, len(row))
		for key, value := range row {
			known := slices.ContainsFunc(table.Columns, func(column query.ColumnDef) bool { return column.Name == key })
			if value != nil || known {
				kept[index][key] = value
			}
		}
	}
	return kept
}
