package sqlite

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"slices"
	"strings"

	"ariga.io/atlas/sql/schema"

	"github.com/flanksource/commons-db/db/sqlitetable"
	sqlitemigrate "github.com/flanksource/commons-db/migrate/sqlite"
	"github.com/flanksource/commons-db/query"
)

// storedColumn is one column entry of a kind's catalog, "declared=physical:SQLTYPE:type":
// a physical column of the kind table and the declared column it stores.
type storedColumn struct {
	declared, physical string

	// storage is the entry after its names, as columnStorage renders it.
	storage string
}

// storedCatalog is a kind's record_kinds entry: one column entry for every
// physical column of its table, in the table's order, then the key a keyed
// kind holds once per stream. A table only ever gains columns, so the entry
// may describe columns the current declaration no longer names — another build
// sharing the file still reads and writes them.
type storedCatalog struct {
	columns []storedColumn
	key     string
}

// parseStoredColumn splits an entry from the right: a physical name is a safe
// identifier and the storage part holds exactly two colons, so the declared
// name before them may contain anything.
func parseStoredColumn(entry string) (storedColumn, bool) {
	typeAt := strings.LastIndex(entry, ":")
	if typeAt < 0 {
		return storedColumn{}, false
	}
	storageAt := strings.LastIndex(entry[:typeAt], ":")
	if storageAt < 0 {
		return storedColumn{}, false
	}
	namesAt := strings.LastIndex(entry[:storageAt], "=")
	if namesAt < 0 {
		return storedColumn{}, false
	}
	return storedColumn{declared: entry[:namesAt], physical: entry[namesAt+1 : storageAt], storage: entry[storageAt:]}, true
}

// def is the declared column the entry stores, with the type it recorded.
func (c storedColumn) def() query.ColumnDef {
	return query.ColumnDef{Name: c.declared, Type: query.ColumnType(c.storage[strings.LastIndex(c.storage, ":")+1:])}
}

// readCatalog reads a kind's catalog entry against its table, and describes,
// as a mismatch, an entry that does not name the table's columns in order.
func readCatalog(ctx context.Context, tx *sql.Tx, table, stored string) (storedCatalog, string, error) {
	var parts []string
	if err := json.Unmarshal([]byte(stored), &parts); err != nil {
		return storedCatalog{}, "", fmt.Errorf("decode catalog columns: %w", err)
	}
	physical, err := sqlitetable.PhysicalColumns(ctx, tx, table)
	if err != nil {
		return storedCatalog{}, "", err
	}
	mismatch := fmt.Sprintf("catalog columns %s naming a table whose columns are %q", stored, physical)
	var catalog storedCatalog
	switch {
	case len(parts) == len(physical):
	case len(parts) == len(physical)+1 && strings.HasPrefix(parts[len(physical)], "key:"):
		catalog.key = strings.TrimPrefix(parts[len(physical)], "key:")
		parts = parts[:len(physical)]
	default:
		return storedCatalog{}, mismatch, nil
	}
	for index, part := range parts {
		column, ok := parseStoredColumn(part)
		if !ok || column.physical != physical[index] {
			return storedCatalog{}, mismatch, nil
		}
		if slices.ContainsFunc(catalog.columns, func(other storedColumn) bool { return other.declared == column.declared }) {
			return storedCatalog{}, fmt.Sprintf("catalog columns %s naming column %q twice", stored, column.declared), nil
		}
		catalog.columns = append(catalog.columns, column)
	}
	return catalog, "", nil
}

// catalogOf is the catalog of a table just created, whose physical columns are
// its declared ones in order.
func catalogOf(table kindTable) storedCatalog {
	catalog := storedCatalog{key: table.schema.Options.Key}
	for index, column := range table.Columns {
		catalog.columns = append(catalog.columns, storedColumn{declared: column.Name, physical: table.StoredAs[index], storage: columnStorage(column)})
	}
	return catalog
}

func (c storedCatalog) encode() (string, error) {
	parts := make([]string, len(c.columns), len(c.columns)+1)
	for index, column := range c.columns {
		parts[index] = column.declared + "=" + column.physical + column.storage
	}
	if c.key != "" {
		parts = append(parts, "key:"+c.key)
	}
	encoded, err := json.Marshal(parts)
	return string(encoded), err
}

// adopt gives table the physical name the catalog recorded for each of its
// declared columns, and returns the declared columns the table does not have.
// A declared column recorded with other storage, or a different key, is a
// mismatch: the rows already written would mean something else.
func (table *kindTable) adopt(catalog storedCatalog) ([]query.ColumnDef, string) {
	if catalog.key != table.schema.Options.Key {
		return nil, fmt.Sprintf("key %q, now %q", catalog.key, table.schema.Options.Key)
	}
	var added []query.ColumnDef
	table.StoredAs = make([]string, len(table.Columns))
	for index, column := range table.Columns {
		at := slices.IndexFunc(catalog.columns, func(stored storedColumn) bool { return stored.declared == column.Name })
		if at < 0 {
			added = append(added, column)
			continue
		}
		if stored, storage := catalog.columns[at].storage, columnStorage(column); stored != storage {
			return nil, fmt.Sprintf("column %q stored as %s, now %s", column.Name, strings.TrimPrefix(stored, ":"), strings.TrimPrefix(storage, ":"))
		}
		table.StoredAs[index] = catalog.columns[at].physical
	}
	return added, ""
}

// addColumns adds to table the declared columns it lacks, under physical names
// derived around every column the table already has. migrate/sqlite reconciles
// them against the table the whole catalog declares, so a table that has
// drifted from its catalog in any other way is refused, not patched. The
// entries are appended to the kind's catalog.
func addColumns(ctx context.Context, tx *sql.Tx, kind string, table *kindTable, catalog storedCatalog, added []query.ColumnDef) error {
	taken := make([]string, len(catalog.columns))
	for index, column := range catalog.columns {
		taken[index] = column.physical
	}
	names := make([]string, len(added))
	for index, column := range added {
		names[index] = column.Name
	}
	physical, err := sqlitetable.PhysicalNames(names, taken, sqlitetable.Bare(ctx, tx))
	if err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	for index, column := range added {
		catalog.columns = append(catalog.columns, storedColumn{declared: column.Name, physical: physical[index], storage: columnStorage(column)})
		table.StoredAs[slices.IndexFunc(table.Columns, func(declared query.ColumnDef) bool { return declared.Name == column.Name })] = physical[index]
	}
	declared, err := catalog.declare(table.Name)
	if err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	if err := sqlitemigrate.ReconcileTables(ctx, tx, sqlitemigrate.ReconcileOptions{}, declared); err != nil {
		return fmt.Errorf("kind %q: %w", kind, err)
	}
	entries, err := catalog.encode()
	if err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `UPDATE record_kinds SET columns = ? WHERE kind = ?`, entries, kind); err != nil {
		return fmt.Errorf("kind %q: record added columns: %w", kind, err)
	}
	return nil
}

// declare is the kind table the catalog describes, as createKindTable creates
// it: every recorded column, the store's primary key, and a keyed kind's
// unique index.
func (c storedCatalog) declare(name string) (*schema.Table, error) {
	table := sqlitetable.Table{Name: name, PrimaryKey: []string{streamColumn, seqColumn}}
	for _, column := range c.columns {
		table.Columns = append(table.Columns, column.def())
		table.StoredAs = append(table.StoredAs, column.physical)
	}
	declared, err := table.Declare()
	if err != nil || c.key == "" {
		return declared, err
	}
	var parts []*schema.Column
	for _, key := range []string{streamColumn, c.key} {
		at := slices.IndexFunc(c.columns, func(column storedColumn) bool { return column.declared == key })
		if at < 0 {
			return nil, fmt.Errorf("table %q keys on %q, which its catalog does not record", name, key)
		}
		parts = append(parts, declared.Columns[at])
	}
	return declared.AddIndexes(schema.NewUniqueIndex(name + "_key").AddColumns(parts...)), nil
}
